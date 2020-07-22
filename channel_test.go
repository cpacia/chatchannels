package channels

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	config "github.com/ipfs/go-ipfs-config"
	"github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/core/bootstrap"
	coremock "github.com/ipfs/go-ipfs/core/mock"
	"github.com/ipfs/go-ipfs/plugin/loader"
	"github.com/ipfs/go-ipfs/repo/fsrepo"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"os"
	"path"
	"strconv"
	"testing"
	"time"
)

func mockNetwork(numNodes int) ([]*core.IpfsNode, func(), error) {
	mn := mocknet.New(context.Background())
	nodes := make([]*core.IpfsNode, 0, numNodes)

	loader, err := loader.NewPluginLoader("")
	if err != nil {
		return nil, nil, err
	}
	err = loader.Initialize()
	if err != nil {
		return nil, nil, err
	}

	err = loader.Inject()
	if err != nil {
		return nil, nil, err
	}

	for i := 0; i < numNodes; i++ {
		dataDir := path.Join(os.TempDir(), "channels_test", strconv.Itoa(i))

		conf, err := config.Init(os.Stdout, 4096)
		if err != nil {
			return nil, nil, err
		}

		if err := fsrepo.Init(dataDir, conf); err != nil {
			return nil, nil, err
		}

		ipfsRepo, err := fsrepo.Open(dataDir)
		if err != nil {
			return nil, nil, err
		}

		ipfsConfig, err := ipfsRepo.Config()
		if err != nil {
			return nil, nil, err
		}

		sk, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		skbytes, err := sk.Bytes()
		if err != nil {
			return nil, nil, err
		}

		id, err := peer.IDFromPublicKey(sk.GetPublic())
		if err != nil {
			return nil, nil, err
		}
		if i > 0 {
			ipfsConfig.Bootstrap = []string{nodes[0].Identity.Pretty()}
		} else {
			ipfsConfig.Bootstrap = nil
		}
		ipfsConfig.Identity = config.Identity{
			PeerID:  id.Pretty(),
			PrivKey: base64.StdEncoding.EncodeToString(skbytes),
		}

		ipfsNode, err := core.NewNode(context.Background(), &core.BuildCfg{
			Online: true,
			Repo:   ipfsRepo,
			Host:   coremock.MockHostOption(mn),
			ExtraOpts: map[string]bool{
				"pubsub": true,
			},
		})
		if err != nil {
			return nil, nil, err
		}

		nodes = append(nodes, ipfsNode)
	}
	tearDown := func() {
		for _, n := range nodes {
			n.Close()
		}
		os.RemoveAll(path.Join(os.TempDir(), "channels_test"))
	}
	if err := mn.LinkAll(); err != nil {
		return nil, nil, err
	}

	bsinf := bootstrap.BootstrapConfigWithPeers(
		[]peer.AddrInfo{
			nodes[0].Peerstore.PeerInfo(nodes[0].Identity),
		},
	)

	for _, n := range nodes[1:] {
		if err := n.Bootstrap(bsinf); err != nil {
			return nil, nil, err
		}
	}
	return nodes, tearDown, nil
}

func TestChannels(t *testing.T) {
	nodes, tearDown, err := mockNetwork(4)
	if err != nil {
		t.Fatal(err)
	}
	defer tearDown()

	cm0, err := NewChannelManager(context.Background(), nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	cm1, err := NewChannelManager(context.Background(), nodes[1])
	if err != nil {
		t.Fatal(err)
	}
	cm2, err := NewChannelManager(context.Background(), nodes[2])
	if err != nil {
		t.Fatal(err)
	}

	ch0, err := cm0.OpenChannel("general")
	if err != nil {
		t.Fatal(err)
	}

	ch1, err := cm1.OpenChannel("general")
	if err != nil {
		t.Fatal(err)
	}

	ch2, err := cm2.OpenChannel("general")
	if err != nil {
		t.Fatal(err)
	}

	<-time.After(time.Second * 1)

	sub0 := ch0.Subscribe()
	sub1 := ch1.Subscribe()
	sub2 := ch2.Subscribe()

	if err := ch0.Publish(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-sub1.Out:
	case <-time.After(time.Second * 10):
		t.Fatal("Sub 1 failed to return")
	}

	select {
	case <-sub2.Out:
	case <-time.After(time.Second * 10):
		t.Fatal("Sub 2 failed to return")
	}

	if err := ch1.Publish(context.Background(), "hello2"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-sub0.Out:
	case <-time.After(time.Second * 10):
		t.Fatal("Sub 1 failed to return")
	}

	select {
	case <-sub2.Out:
	case <-time.After(time.Second * 10):
		t.Fatal("Sub 2 failed to return")
	}

	messages, err := ch2.Messages(context.Background(), nil, -1)
	if err != nil {
		t.Fatal(err)
	}

	if len(messages) != 2 {
		t.Errorf("Expected 2 messages got %d", len(messages))
	}

	cm3, err := NewChannelManager(context.Background(), nodes[3])
	if err != nil {
		t.Fatal(err)
	}

	ch3, err := cm3.OpenChannel("general")
	if err != nil {
		t.Fatal(err)
	}

	<-time.After(time.Second * 1)

	messages, err = ch3.Messages(context.Background(), nil, -1)
	if err != nil {
		t.Fatal(err)
	}

	if len(messages) != 2 {
		t.Errorf("Expected 2 messages got %d", len(messages))
	}
}
