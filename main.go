package main

import (
	"context"
	"os"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/qzhodl/polling-client/client"
)

func main() {
	logger := log.New()
	logger.SetHandler(log.StreamHandler(os.Stdout, log.TerminalFormat(true)))

	// Use the logger to write log messages
	logger.Info("This is an info message")

	c, err := client.Dial("wss://eth-mainnet.g.alchemy.com/v2/XXX", logger)
	if err != nil {
		logger.Crit("client creating failed")
	}

	ctx := context.Background()
	headChanges := make(chan *types.Header, 10)
	_, err = c.SubscribeNewHead(ctx, headChanges)
	if err != nil {
		logger.Crit("client creating failed")
	}

	for {
		head := <-headChanges
		logger.Trace("New Block Number is", "head", head.Number)
	}
}
