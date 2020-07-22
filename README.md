chatchannels
====
The repo contains a peer-to-peer chat room library built on top of IPFS and libp2p. 

Peers "subscribe" to a channel by appending their peer info (IP addresses / public key) to the channel's 
key in the DHT. This allows other peers to find the list of subscribers and connect to a few of them. 
Each subscriber ends up indirectly connected to every other subscriber creating a network over which
chat messages can be relayed. 

Each peer saves the cid of the last message(s) it knows about. When a peer publishes a new message
to the channel it links to the previous message. This essentially forms a linked list of messages:

```
m0 <- m1 <- m2 <- m3
```

Given the asynchronous nature of the network it is possible two different peers will attempt to publish messages
at around the same time, linking to the same previous message and creating a race condition. We handle this by saving
multiple cids at the head if necessary and linking to multiple previous messages. This means that the linked list may 
fan out into a DAG during periods of high message throughput before collapsing back to a linear chain during periods of
downtime. For example:

```
                 m3a
               /    \
m0 <- m1 <- m2        m4 <- m5
               \    /
                 m3b 
```

Since each message is an IPFS object loading the message history of the channel is just a matter of traversing the DAG backwards from the head of the channel.

## Usage

```go
// Create a new channel manager which will manage the networking. You 
// must pass in an instantiate IPFS node object (github.com/ipfs/go-ipfs/core.(IpfsNode)).
manager, err := NewChannelManager(context.Background(), ipfsNode)
if err != nil {
	return err
}

// Open a channel called 'general'.
channel, err := manager.OpenChannel("general")
if err != nil {
	return err
}

// Publish a message to the channel.
if err := channel.Publish(context.Background(), "Hello world"); err != nil {
	return err
}

// Fetch the last 20 messages from the channel.
// The second parameter is a cid offset if you want to fetch subsequent batches.
messages, err := channel.Messages(context.Backround(), nil, 20)
if err != nil {
	return err
}

// Subscribe to new messages that come off the wire.
sub := channel.Subscribe()
for {
	select {
	case message := <- sub
	    fmt.Println(message)
	}
}
```
