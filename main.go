package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sync"

	"net/url"
	"os"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/multiformats/go-multiaddr"
	jrpc "github.com/ybbus/jsonrpc/v2"
)

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

type MarketBalance struct {
	Escrow big.Int
	Locked big.Int
}

const defaultGateway = "api.node.glif.io"
const maxRoutines = 20

func main() {
	// Subcommands
	populateCommand := flag.NewFlagSet("populate", flag.ExitOnError)
	findCommand := flag.NewFlagSet("find", flag.ExitOnError)
	queryAsksCommand := flag.NewFlagSet("query-asks", flag.ExitOnError)

	// Populate subcommand flag pointers
	populateGatewayPtr := populateCommand.String("gateway", defaultGateway, "Gateway URL")
	// find subcommand flag pointers
	findSpIdPtr := findCommand.String("storage_provider_id", "", "Storage Provider ID (Required)")
	findGatewayPtr := findCommand.String("gateway", defaultGateway, "Gateway URL")
	// Query asks subcommand flag pointers
	queryAsksGatewayPtr := queryAsksCommand.String("gateway", defaultGateway, "Gateway URL")

	// Verify that a subcommand has been provided
	// os.Arg[0] is the main command
	// os.Arg[1] will be the subcommand
	if len(os.Args) < 2 {
		fmt.Println("populate, find, query-asks subcommand is required")
		os.Exit(1)
	}

	// Switch on the subcommand
	// Parse the flags for appropriate FlagSet
	switch os.Args[1] {
	case "find":
		findCommand.Parse(os.Args[2:])
	case "populate":
		populateCommand.Parse(os.Args[2:])
	case "query-asks":
		queryAsksCommand.Parse(os.Args[2:])
	default:
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Check which subcommand was Parsed using the FlagSet.Parsed() function. Handle each case accordingly.
	// FlagSet.Parse() will evaluate to false if no flags were parsed (i.e. the user did not provide any flags)
	if findCommand.Parsed() {
		// Required Flags
		if *findSpIdPtr == "" {
			findCommand.PrintDefaults()
			os.Exit(1)
		}
		spid := *findSpIdPtr
		gateway := *findGatewayPtr

		addrInfo, minerList, err := spidToAddrInfo(context.Background(), gateway, spid)
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

		fmt.Println("Miner List Size: ", len(minerList))
	}

	if populateCommand.Parsed() {
		gateway := *populateGatewayPtr
		fmt.Println("Populating...")
		err := populateMinerPeerIds(gateway)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	if queryAsksCommand.Parsed() {
		gateway := *queryAsksGatewayPtr
		fmt.Println("Populating...")
		err := queryAskMiners(gateway)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func spidToAddrInfo(ctx context.Context, gateway, spid string) (peer.AddrInfo, map[string]MarketBalance, error) {
	u := url.URL{
		Host:   gateway,
		Scheme: "https",
		Path:   "/rpc/v0",
	}
	gatewayURL := u.String()

	// Get miner info from lotus
	spAddress, err := address.NewFromString(spid)
	if err != nil {
		return peer.AddrInfo{}, nil, fmt.Errorf("invalid provider filecoin address: %s", err)
	}

	jrpcClient := jrpc.NewClient(gatewayURL)

	var ets ExpTipSet
	err = jrpcClient.CallFor(&ets, "Filecoin.ChainHead")
	if err != nil {
		return peer.AddrInfo{}, nil, err
	}

	var minerInfo MinerInfo
	err = jrpcClient.CallFor(&minerInfo, "Filecoin.StateMinerInfo", spAddress, ets.Cids)
	if err != nil {
		return peer.AddrInfo{}, nil, err
	}

	minerList := make(map[string]MarketBalance)
	err = jrpcClient.CallFor(&minerList, "Filecoin.StateMarketParticipants", nil)
	if err != nil {
		return peer.AddrInfo{}, nil, err
	}

	if minerInfo.PeerId == nil {
		return peer.AddrInfo{}, nil, errors.New("no peer id for service provider")
	}

	// Get miner peer ID and addresses from miner info
	addrInfo, err := minerInfoToAddrInfo(minerInfo)
	if err != nil {
		return peer.AddrInfo{}, nil, err
	}

	return addrInfo, minerList, err
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

func minerListToPeerId(minerList map[string]MarketBalance, jrpcClient jrpc.RPCClient) (map[string]peer.ID, error) {
	minerIdToPeerId := make(map[string]peer.ID)
	minerChan := make(chan string)
	resultChan := make(chan string)
	var wg sync.WaitGroup
	wg.Add(maxRoutines)
	for i := 0; i < maxRoutines; i++ {
		go func() {
			for minerId := range minerChan {
				resultChan <- printMinerIdPeerId(minerId, jrpcClient)
			}
			wg.Done()
		}()
	}
	go func() {
		for out := range resultChan {
			fmt.Print(out)
		}
	}()
	for k := range minerList {
		minerChan <- k
	}
	close(minerChan)
	wg.Wait()
	close(resultChan)

	return minerIdToPeerId, nil
}

func minerListToQueryAsks(minerList map[string]MarketBalance, jrpcClient jrpc.RPCClient) (map[string]string, error) {
	minerIdToQueryAsks := make(map[string]string)
	minerChan := make(chan string)
	resultChan := make(chan string)
	var wg sync.WaitGroup
	wg.Add(maxRoutines)
	for i := 0; i < maxRoutines; i++ {
		go func() {
			for minerId := range minerChan {
				resultChan <- printMinerQueryAskResult(minerId, jrpcClient)
			}
			wg.Done()
		}()
	}
	go func() {
		for out := range resultChan {
			fmt.Print(out)
		}
	}()
	for k := range minerList {
		minerChan <- k
	}
	close(minerChan)
	wg.Wait()
	close(resultChan)

	return minerIdToQueryAsks, nil
}

func printMinerIdPeerId(minerId string, jrpcClient jrpc.RPCClient) string {
	var minerInfo MinerInfo
	err := jrpcClient.CallFor(&minerInfo, "Filecoin.StateMinerInfo", minerId, nil)

	if err != nil {
		return fmt.Sprintln(minerId, err)
	}
	if minerInfo.PeerId == nil {
		return fmt.Sprintln(minerId, "has no peer ID")
	}
	return fmt.Sprintln(minerId, " -> ", *minerInfo.PeerId)
}

func printMinerQueryAskResult(minerId string, jrpcClient jrpc.RPCClient) string {
	var minerInfo MinerInfo
	err := jrpcClient.CallFor(&minerInfo, "Filecoin.StateMinerInfo", minerId, nil)

	if err != nil {
		return fmt.Sprintln(minerId, err)
	}
	if minerInfo.PeerId == nil {
		return fmt.Sprintln(minerId, "has no peer ID")
	}

	var queryAskResult string
	err = jrpcClient.CallFor(&queryAskResult, "Filecoin.ClientQueryAsk", minerInfo.PeerId, minerId)

	if err != nil {
		return fmt.Sprintln(minerId, err)
	}
	if queryAskResult == "" {
		return fmt.Sprintln(minerId, "has no query ask result")
	}
	return fmt.Sprintln(minerId, " -> ", queryAskResult)
}

func populateMinerPeerIds(gateway string) error {
	u := url.URL{
		Host:   gateway,
		Scheme: "https",
		Path:   "/rpc/v0",
	}
	gatewayURL := u.String()
	jrpcClient := jrpc.NewClient(gatewayURL)

	minerList := make(map[string]MarketBalance)
	err := jrpcClient.CallFor(&minerList, "Filecoin.StateMarketParticipants", nil)
	if err != nil {
		return err
	}

	mIdPeerIdMap, err := minerListToPeerId(minerList, jrpcClient)
	fmt.Println("Miner-PeerId List:")
	for k, v := range mIdPeerIdMap {
		fmt.Printf("%s -> %s\n", k, v)
	}

	return err
}

func queryAskMiners(gateway string) error {
	u := url.URL{
		Host:   gateway,
		Scheme: "https",
		Path:   "/rpc/v0",
	}
	gatewayURL := u.String()
	jrpcClient := jrpc.NewClient(gatewayURL)

	minerList := make(map[string]MarketBalance)
	err := jrpcClient.CallFor(&minerList, "Filecoin.StateMarketParticipants", nil)
	if err != nil {
		return err
	}

	mIdQueryAskMap, err := minerListToQueryAsks(minerList, jrpcClient)
	fmt.Println("Miner-QueryAsk List:")
	for k, v := range mIdQueryAskMap {
		fmt.Printf("%s -> %s\n", k, v)
	}
	return err
}
