package channels

import (
	"fmt"
	ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p-core/protocol"
)

// ProtocolChannels is the protocol ID used by libp2p.
const ProtocolChannels protocol.ID = "/libp2p/chatchannels/0.1.0"

var (
	defaultProtocols = []protocol.ID{ProtocolChannels}
)

// Options is a structure containing all the options that can be used when constructing a Store and Forward node.
type Options struct {
	Datastore ds.Batching
	Protocols []protocol.ID
}

// Apply applies the given options to this Option
func (o *Options) Apply(opts ...Option) error {
	for i, opt := range opts {
		if err := opt(o); err != nil {
			return fmt.Errorf("snf option %d failed: %s", i, err)
		}
	}
	return nil
}

// Option Store and Forward option type.
type Option func(*Options) error

// Defaults are the default options. This option will be automatically
// prepended to any options you pass to the constructor.
var Defaults = func(o *Options) error {
	o.Datastore = nil
	o.Protocols = defaultProtocols
	return nil
}

// Datastore configures the Server to use the specified datastore.
//
// Defaults to nil which will use the IPFS node's datastore.
func Datastore(ds ds.Batching) Option {
	return func(o *Options) error {
		o.Datastore = ds
		return nil
	}
}

// Protocols sets the protocols for the Store and Forward nodes.
//
// Defaults to defaultProtocols
func Protocols(protocols ...protocol.ID) Option {
	return func(o *Options) error {
		o.Protocols = protocols
		return nil
	}
}
