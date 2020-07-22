package channels

import (
	"bytes"
	"context"
	"errors"
	"github.com/cpacia/chatchannels/pb"
	ggio "github.com/gogo/protobuf/io"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/core/coreapi"
	"github.com/ipfs/go-merkledag"
	iface "github.com/ipfs/interface-go-ipfs-core"
	caopts "github.com/ipfs/interface-go-ipfs-core/options"
	"github.com/ipfs/interface-go-ipfs-core/path"
	ctxio "github.com/jbenet/go-context/io"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	inet "github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	mrand "math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	topicPrefix       = "/channel/"
	maxBootstrapPeers = 5
)

// Subscription provides a chan over which new messages are pushed.
type Subscription struct {
	Out   chan *pb.ChannelMessage
	Close func()
}

// Channel represents an open chat channel.
type Channel struct {
	pubsub   iface.PubSubAPI
	object   iface.ObjectAPI
	topic    string
	ds       datastore.Batching
	host     host.Host
	protocol protocol.ID
	privKey  crypto.PrivKey
	identity peer.ID
	subs     map[int64]*Subscription
	subMtx   sync.RWMutex
	cache    map[cid.Cid]bool
	cacheMtx sync.RWMutex

	boostrapped bool
	shutdown    chan struct{}
}

// newChannel instantiates a new chat channel, subscribes to the pubsub topic, and bootstraps the initial messages.
func newChannel(topic string, ipfsNode *core.IpfsNode, ds datastore.Batching, protocol protocol.ID) (*Channel, error) {
	api, err := coreapi.NewCoreAPI(ipfsNode)
	if err != nil {
		return nil, err
	}

	c := &Channel{
		pubsub:   api.PubSub(),
		object:   api.Object(),
		topic:    strings.ToLower(topic),
		ds:       ds,
		host:     ipfsNode.PeerHost,
		protocol: protocol,
		privKey:  ipfsNode.PrivateKey,
		identity: ipfsNode.Identity,
		subs:     make(map[int64]*Subscription),
		subMtx:   sync.RWMutex{},
		cache:    make(map[cid.Cid]bool),
		cacheMtx: sync.RWMutex{},
		shutdown: make(chan struct{}),
	}
	if err := c.run(); err != nil {
		return nil, err
	}
	return c, nil
}

// Subscribe returns a new Subscription to this channel.
func (c *Channel) Subscribe() *Subscription {
	i := mrand.Int63()

	sub := &Subscription{
		Out: make(chan *pb.ChannelMessage),
	}
	sub.Close = func() {
		close(sub.Out)
		c.subMtx.Lock()
		delete(c.subs, i)
		c.subMtx.Unlock()
	}

	c.subMtx.Lock()
	c.subs[i] = sub
	c.subMtx.Unlock()
	return sub
}

// Topic returns the topic of this channel.
func (c *Channel) Topic() string {
	return c.topic
}

// Publish broadcasts a message to this chat channel. The message will contain
// a pointer to the previous message(s) in the channel so that the channel
// history can be loaded by traversing the DAG backwards.
func (c *Channel) Publish(ctx context.Context, message string) error {
	msg := &pb.ChannelMessage{
		Message:   message,
		Topic:     c.topic,
		PeerID:    c.identity.Pretty(),
		Timestamp: ptypes.TimestampNow(),
	}
	ser, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	sig, err := c.privKey.Sign(ser)
	if err != nil {
		return err
	}
	msg.Signature = sig

	serializedMsg, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	nd, err := c.object.New(ctx)
	if err != nil {
		return err
	}

	pth, err := c.object.SetData(ctx, path.IpldPath(nd.Cid()), bytes.NewReader(serializedMsg))
	if err != nil {
		return err
	}

	val, err := c.ds.Get(datastore.NewKey(channelKeyPrefix + c.topic))
	if err != nil && err != datastore.ErrNotFound {
		return err
	}
	head := Head(val)
	links, err := head.GetLinks()
	if err != nil {
		return err
	}
	for i, link := range links {
		pth, err = c.object.AddLink(ctx, pth, "previousMessage"+strconv.Itoa(i), path.IpldPath(link))
		if err != nil {
			return err
		}
	}

	ind, err := c.object.Get(ctx, pth)
	if err != nil {
		return err
	}

	pnd, ok := ind.(*merkledag.ProtoNode)
	if !ok {
		return errors.New("protoNode type assertion error")
	}

	serializedObj, err := pnd.Marshal()
	if err != nil {
		return err
	}

	return c.pubsub.Publish(ctx, topicPrefix+c.topic, serializedObj)
}

// Messages returns the next `limit` messages starting from the `from` cid.
// If no cid is provided this will return messages starting at the head of
// the channel. If no more messages could be found it will return a nil slice.
func (c *Channel) Messages(ctx context.Context, from *cid.Cid, limit int) ([]pb.ChannelMessage, error) {
	if limit <= 0 {
		limit = 20
	}

	level := make(map[cid.Cid]bool)
	if from != nil {
		c.cacheMtx.RLock()
		if c.cache[*from] {
			level = c.cache
		}
		c.cacheMtx.RUnlock()
	} else {
		val, err := c.ds.Get(datastore.NewKey(channelKeyPrefix + c.topic))
		if err != nil && err != datastore.ErrNotFound {
			return nil, err
		}
		if val == nil {
			return nil, nil
		}
		head := Head(val)
		links, err := head.GetLinks()
		if err != nil {
			return nil, err
		}

		for _, id := range links {
			level[id] = true
		}
	}

	ret := make([]pb.ChannelMessage, 0, limit)
	for {
		nextLevel := make(map[cid.Cid]bool)
		for id := range level {
			nd, err := c.object.Get(ctx, path.IpldPath(id))
			if err != nil {
				continue
			}
			pnd, ok := nd.(*merkledag.ProtoNode)
			if !ok {
				continue
			}

			var cm pb.ChannelMessage
			if err := proto.Unmarshal(pnd.Data(), &cm); err != nil {
				continue
			}

			valid, err := validateMessage(&cm)
			if err != nil || !valid {
				continue
			}
			cm.Cid = pnd.Cid().Bytes()
			ret = append(ret, cm)

			for _, link := range nd.Links() {
				nextLevel[link.Cid] = true
			}
		}
		if len(nextLevel) == 0 || len(ret) >= limit {
			break
		}
		level = nextLevel
	}
	c.cacheMtx.Lock()
	c.cache = level
	c.cacheMtx.Unlock()

	sort.Slice(ret, func(i, j int) bool {
		jTime := time.Unix(ret[j].Timestamp.Seconds, int64(ret[j].Timestamp.Nanos))
		iTime := time.Unix(ret[i].Timestamp.Seconds, int64(ret[i].Timestamp.Nanos))
		return jTime.Before(iTime)
	})

	return ret, nil
}

// Close will shutdown the channel.
func (c *Channel) Close() {
	close(c.shutdown)
}

// run is subscribing to the pubsub topic and bootstrapping the last
// known messages in the channel. If a new message is received on the channel
// before the bootstrap finishes we will set that message as the head and
// terminate the bootstrapping.
func (c *Channel) run() error {
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := c.pubsub.Subscribe(ctx, topicPrefix+c.topic, caopts.PubSub.Discover(true))
	if err != nil {
		log.Errorf("Error subscribing to channel, topic %s: %s", c.topic, err)
		return err
	}

	go c.bootstrapState()

	go func() {
		for {
			msg, err := sub.Next(ctx)
			if err != nil && err != context.Canceled {
				log.Errorf("Error fetching next channel message, topic %s: %s", c.topic, err)
				continue
			}
			if err == context.Canceled {
				return
			}

			pnd, err := merkledag.DecodeProtobuf(msg.Data())
			if err != nil {
				log.Errorf("Error decoding channel object, topic %s: %s", c.topic, err)
				continue
			}

			channelMsg := new(pb.ChannelMessage)
			if err := proto.Unmarshal(pnd.Data(), channelMsg); err != nil {
				log.Errorf("Error decoding channel protobuf, topic %s: %s", c.topic, err)
				continue
			}

			valid, err := validateMessage(channelMsg)
			if err != nil {
				log.Error("Error validating message, topic %s: %s", c.topic, err)
				continue
			}
			if !valid {
				log.Error("Message invalid, topic %s: %s", c.topic, err)
				continue
			}

			pth, err := c.object.Put(context.Background(), bytes.NewReader(msg.Data()), caopts.Object.InputEnc("protobuf"), caopts.Object.Pin(true))
			if err != nil {
				log.Errorf("Error putting message to IPFS, topic %s: peer %s: %s", c.topic, channelMsg.PeerID, err)
				continue
			}

			nd, err := c.object.Get(context.Background(), pth)
			if err != nil {
				log.Errorf("Error getting IPFS object, topic %s: peer %s: %s", c.topic, channelMsg.PeerID, err)
				continue
			}

			wasBoostrapped := c.boostrapped
			val, err := c.ds.Get(datastore.NewKey(channelKeyPrefix + c.topic))
			if err != nil && err != datastore.ErrNotFound {
				log.Errorf("Error updating database with new cids, topic %s: peer %s: %s", c.topic, channelMsg.PeerID, err)
				continue
			}
			head := Head(val)
			if !c.boostrapped {
				err = head.SetHead([]cid.Cid{nd.Cid()})
			} else {
				err = head.UpdateHead(nd)
			}
			if err != nil {
				log.Errorf("Error updating database with new cids, topic %s: peer %s: %s", c.topic, channelMsg.PeerID, err)
				continue
			}
			if err := c.ds.Put(datastore.NewKey(channelKeyPrefix+c.topic), head); err != nil {
				log.Errorf("Error updating database with new cids, topic %s: peer %s: %s", c.topic, channelMsg.PeerID, err)
				continue
			}

			if !wasBoostrapped {
				log.Infof("Bootstrapped channel %s with %d cids", c.topic, 1)
			}

			c.boostrapped = true
			channelMsg.Cid = nd.Cid().Bytes()

			c.subMtx.RLock()
			for _, sub := range c.subs {
				sub.Out <- channelMsg
			}
			c.subMtx.RUnlock()
		}
	}()

	go func() {
		<-c.shutdown
		cancel()
		sub.Close()
	}()

	return nil
}

// bootstrapState loops until some channel peers connect. Once they connect
// it queries each of them for the cid(s) they believe to be the head of the
// channel. The responses are set as the head in our database.
func (c *Channel) bootstrapState() {
	defer func() {
		c.boostrapped = true
	}()
	var (
		peers []peer.ID
		err   error
	)
	ticker := time.NewTicker(time.Second * 4)
	for ; true; <-ticker.C {
		if c.boostrapped {
			return
		}
		peers, err = c.pubsub.Peers(context.Background(), caopts.PubSub.Topic(topicPrefix+c.topic))
		if err != nil {
			log.Debugf("No pubsub peers found for topic: %s", c.topic)
			continue
		}
		if len(peers) > 0 {
			break
		}
	}

	max := len(peers)
	if max > maxBootstrapPeers {
		max = maxBootstrapPeers
	}

	respChan := make(chan []cid.Cid)
	var wg sync.WaitGroup
	wg.Add(max)
	log.Debugf("Bootstrapping channel %s with %d peers", c.topic, max)
	go func() {
		for _, p := range peers[:max] {
			go func(pid peer.ID) {
				defer wg.Done()
				req := &pb.NetworkMessage{
					Type: pb.NetworkMessage_BOOTSTRAP_REQUEST,
					Payload: &pb.NetworkMessage_Request{
						Request: &pb.NetworkMessage_BootstrapRequest{
							Topic: c.topic,
						},
					},
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
				defer cancel()
				stream, err := c.host.NewStream(ctx, pid, c.protocol)
				if err != nil {
					log.Errorf("Error opening bootstrap stream to %s: %s", pid.Pretty(), err)
					return
				}
				contextReader := ctxio.NewReader(ctx, stream)
				reader := ggio.NewDelimitedReader(contextReader, inet.MessageSizeMax)
				writer := ggio.NewDelimitedWriter(stream)

				if err := writer.WriteMsg(req); err != nil {
					log.Errorf("Error writing message to peer %s: %s", pid.Pretty(), err)
					return
				}
				pmes := new(pb.NetworkMessage)
				if err := reader.ReadMsg(pmes); err != nil {
					log.Errorf("Error reading bootstrap response from peer %s: %s", pid.Pretty(), err)
					return
				}

				if pmes.Type != pb.NetworkMessage_BOOTSTRAP_RESPONSE {
					log.Errorf("Peer %s responded with incorrect message type", pid.Pretty())
					return
				}

				if pmes.GetResponse() == nil {
					log.Errorf("Peer %s responded with nil response", pid.Pretty())
					return
				}

				resp := make([]cid.Cid, 0, len(pmes.GetResponse().Cids))
				for _, buf := range pmes.GetResponse().Cids {
					_, id, err := cid.CidFromBytes(buf)
					if err != nil {
						continue
					}
					resp = append(resp, id)
				}

				respChan <- resp
			}(p)
		}
		wg.Wait()
		close(respChan)
	}()

	cidMap := make(map[cid.Cid]bool)
	for resp := range respChan {
		for _, id := range resp {
			cidMap[id] = true
		}
	}
	ids := make([]cid.Cid, 0, len(cidMap))
	for id := range cidMap {
		ids = append(ids, id)
	}

	if c.boostrapped || len(ids) == 0 {
		return
	}

	var head Head
	if err := head.SetHead(ids); err != nil {
		log.Errorf("Error updating db with cids from peers, topic %s: %s", c.topic, err)
		return
	}

	if err := c.ds.Put(datastore.NewKey(channelKeyPrefix+c.topic), head); err != nil {
		log.Errorf("Error updating database with new cids, topic %s: %s", c.topic, err)
		return
	}

	log.Infof("Bootstrapped channel %s with %d cids", c.topic, len(ids))
}

func validateMessage(cm *pb.ChannelMessage) (bool, error) {
	cloneMsg := proto.Clone(cm)
	cloneMsg.(*pb.ChannelMessage).Signature = nil
	ser, err := proto.Marshal(cloneMsg)
	if err != nil {
		return false, err
	}
	peerID, err := peer.Decode(cm.PeerID)
	if err != nil {
		return false, err
	}
	pubkey, err := peerID.ExtractPublicKey()
	if err != nil {
		return false, err
	}
	valid, err := pubkey.Verify(ser, cm.Signature)
	if err != nil {
		return false, err
	}
	if !valid {
		return false, nil
	}
	return true, nil
}
