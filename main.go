package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/filecoin-project/go-address"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/multiformats/go-multiaddr"
	jrpc "github.com/ybbus/jsonrpc/v2"
)

type Discoverer struct {
	gatewayURL string
}

type ExpTipSet struct {
	Cids []cid.Cid
	//Blocks []*BlockHeader
	//Height abi.ChainEpoch
	Blocks []interface{}
	Height int64
}

type MinerInfo struct {
	Owner                      address.Address
	Worker                     address.Address
	NewWorker                  address.Address
	ControlAddresses           []address.Address
	WorkerChangeEpoch          int64
	PeerId                     *peer.ID
	Multiaddrs                 [][]byte
	WindowPoStProofType        int64
	SectorSize                 uint64
	WindowPoStPartitionSectors uint64
	ConsensusFaultElapsed      int64
}

const defaultGateway = "api.chain.love"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "missing storage provider id")
		fmt.Fprintln(os.Stderr, "usage:", os.Args[0], "storage_provider_id [gateway_addr]")
		fmt.Fprintln(os.Stderr, "example:", os.Args[0], "t01000", defaultGateway)
		os.Exit(1)
	}
	spid := os.Args[1]
	gateway := defaultGateway
	if len(os.Args) > 2 {
		gateway = os.Args[2]
	}

	addrInfo, err := spidToAddrInfo(context.Background(), gateway, spid)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("PeerID:", addrInfo.ID)
	if len(addrInfo.Addrs) != 0 {
		fmt.Println("Addrs:")
		for _, a := range addrInfo.Addrs {
			fmt.Println("  ", a)
		}
	}
}

func spidToAddrInfo(ctx context.Context, gateway, spid string) (peer.AddrInfo, error) {
	u := url.URL{
		Host:   gateway,
		Scheme: "https",
		Path:   "/rpc/v1",
	}
	gatewayURL := u.String()

	// Get miner info from lotus
	spAddress, err := address.NewFromString(spid)
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("invalid provider filecoin address: %s", err)
	}

	jrpcClient := jrpc.NewClient(gatewayURL)

	var ets ExpTipSet
	err = jrpcClient.CallFor(&ets, "Filecoin.ChainHead")
	if err != nil {
		return peer.AddrInfo{}, err
	}

	var minerInfo MinerInfo
	err = jrpcClient.CallFor(&minerInfo, "Filecoin.StateMinerInfo", spAddress, ets.Cids)
	if err != nil {
		return peer.AddrInfo{}, err
	}

	if minerInfo.PeerId == nil {
		return peer.AddrInfo{}, errors.New("no peer id for service provder")
	}

	// Get miner peer ID and addresses from miner info
	return minerInfoToAddrInfo(minerInfo)
}

func minerInfoToAddrInfo(minerInfo MinerInfo) (peer.AddrInfo, error) {
	multiaddrs := make([]multiaddr.Multiaddr, 0, len(minerInfo.Multiaddrs))
	for _, a := range minerInfo.Multiaddrs {
		maddr, err := multiaddr.NewMultiaddrBytes(a)
		if err != nil {
			continue
		}
		multiaddrs = append(multiaddrs, maddr)
	}

	return peer.AddrInfo{
		ID:    *minerInfo.PeerId,
		Addrs: multiaddrs,
	}, nil
}
