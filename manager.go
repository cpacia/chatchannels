package channels

import (
	"context"
	"errors"
	"github.com/cpacia/chatchannels/pb"
	ggio "github.com/gogo/protobuf/io"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-ipfs/core"
	logging "github.com/ipfs/go-log"
	ctxio "github.com/jbenet/go-context/io"
	inet "github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/protocol"
	"io"
)

const (
	channelKeyPrefix = "/cs/channel/"
)

var log = logging.Logger("channels")

// ChannelManager controls the creation of new channels and responds to
// bootstrap requests from other peers.
type ChannelManager struct {
	ctx      context.Context
	ds       datastore.Batching
	node     *core.IpfsNode
	protocol protocol.ID
}

// NewChannelManager returns a new channel manager.
func NewChannelManager(ctx context.Context, n *core.IpfsNode, opts ...Option) (*ChannelManager, error) {
	var cfg Options
	if err := cfg.Apply(append([]Option{Defaults}, opts...)...); err != nil {
		return nil, err
	}

	if len(cfg.Protocols) == 0 {
		return nil, errors.New("protocol option is required")
	}

	ds := cfg.Datastore
	if ds == nil {
		ds = n.Repo.Datastore()
	}

	cm := &ChannelManager{
		ctx:      ctx,
		ds:       ds,
		protocol: cfg.Protocols[0],
		node:     n,
	}

	for _, protocol := range cfg.Protocols {
		n.PeerHost.SetStreamHandler(protocol, cm.handleNewStream)
	}

	return cm, nil
}

// OpenChannel opens and bootstraps a new channel for the given topic.
func (cm *ChannelManager) OpenChannel(topic string) (*Channel, error) {
	return newChannel(topic, cm.node, cm.ds, cm.protocol)
}

func (cm *ChannelManager) handleNewStream(s inet.Stream) {
	go cm.streamHandler(s)
}

func (cm *ChannelManager) streamHandler(s inet.Stream) {
	defer s.Close()

	contextReader := ctxio.NewReader(cm.ctx, s)
	reader := ggio.NewDelimitedReader(contextReader, inet.MessageSizeMax)
	writer := ggio.NewDelimitedWriter(s)
	remotePeer := s.Conn().RemotePeer()

	pmes := new(pb.NetworkMessage)
	if err := reader.ReadMsg(pmes); err != nil {
		s.Reset()
		if err == io.EOF {
			log.Debugf("peer %s closed stream", remotePeer)
		}
		return
	}

	var err error
	switch pmes.Type {
	case pb.NetworkMessage_BOOTSTRAP_REQUEST:
		err = cm.handleBootstrapRequest(writer, pmes)
	default:
		err = errors.New("unknown message type")
	}
	if err != nil {
		log.Errorf("Peer %s: Error handling %s message: %s", remotePeer, pmes.Type, err)
	}
}

func (cm *ChannelManager) handleBootstrapRequest(w ggio.Writer, pmes *pb.NetworkMessage) error {
	if pmes.GetRequest() == nil {
		return errors.New("bootstrap request is nil")
	}

	val, err := cm.ds.Get(datastore.NewKey(channelKeyPrefix + pmes.GetRequest().Topic))
	if err != nil && err != datastore.ErrNotFound {
		return err
	}

	h := Head(val)

	cids, err := h.GetLinks()
	if err != nil {
		return err
	}

	ret := make([][]byte, 0, len(cids))
	for _, id := range cids {
		ret = append(ret, id.Bytes())
	}

	resp := &pb.NetworkMessage{
		Type: pb.NetworkMessage_BOOTSTRAP_RESPONSE,
		Payload: &pb.NetworkMessage_Response{
			Response: &pb.NetworkMessage_BootstrapResponse{
				Topic: pmes.GetRequest().Topic,
				Cids:  ret,
			},
		},
	}

	return w.WriteMsg(resp)
}
