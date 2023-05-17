package client

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

var httpRegex = regexp.MustCompile("^http(s)?://")
var ErrSubscriberClosed = errors.New("subscriber closed")

type PollingClient struct {
	*ethclient.Client
	isHTTP   bool
	lgr      log.Logger
	pollRate time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
	currHead *types.Header
	subID    int

	// pollReqCh is used to request new polls of the upstream
	// RPC client.
	pollReqCh chan struct{}

	mtx sync.RWMutex

	subs map[int]chan *types.Header

	closedCh chan struct{}
}

// Dial connects a client to the given URL.
func Dial(rawurl string, lgr log.Logger) (*PollingClient, error) {
	return DialContext(context.Background(), rawurl, lgr)
}

func DialContext(ctx context.Context, rawurl string, lgr log.Logger) (*PollingClient, error) {
	c, err := ethclient.DialContext(ctx, rawurl)
	if err != nil {
		return nil, err
	}
	return NewClient(ctx, c, httpRegex.MatchString(rawurl), lgr), nil
}

// NewClient creates a client that uses the given RPC client.
func NewClient(ctx context.Context, c *ethclient.Client, isHTTP bool, lgr log.Logger) *PollingClient {
	ctx, cancel := context.WithCancel(ctx)
	res := &PollingClient{
		Client:    c,
		isHTTP:    isHTTP,
		lgr:       lgr,
		pollRate:  12 * time.Second,
		ctx:       ctx,
		cancel:    cancel,
		pollReqCh: make(chan struct{}, 1),
		subs:      make(map[int]chan *types.Header),
		closedCh:  make(chan struct{}),
	}
	if isHTTP {
		go res.pollHeads()
	}
	return res
}

// Close closes the PollingClient and the underlying RPC client it talks to.
func (w *PollingClient) Close() {
	w.cancel()
	<-w.closedCh
	w.Client.Close()
}

func (w *PollingClient) SubscribeNewHead(ctx context.Context, ch chan<- *types.Header) (ethereum.Subscription, error) {
	if !w.isHTTP {
		return w.Client.SubscribeNewHead(ctx, ch)
	}
	select {
	case <-w.ctx.Done():
		return nil, ErrSubscriberClosed
	default:
	}

	sub := make(chan *types.Header, 1)
	w.mtx.Lock()
	subID := w.subID
	w.subID++
	w.subs[subID] = sub
	w.mtx.Unlock()

	return event.NewSubscription(func(quit <-chan struct{}) error {
		for {
			select {
			case header := <-sub:
				ch <- header
			case <-quit:
				w.mtx.Lock()
				delete(w.subs, subID)
				w.mtx.Unlock()
				return nil
			case <-w.ctx.Done():
				return nil
			}
		}
	}), nil
}

func (w *PollingClient) pollHeads() {
	// To prevent polls from stacking up in case HTTP requests
	// are slow, use a similar model to the driver in which
	// polls are requested manually after each header is fetched.
	reqPollAfter := func() {
		if w.pollRate == 0 {
			return
		}
		time.AfterFunc(w.pollRate, w.reqPoll)
	}

	reqPollAfter()

	defer close(w.closedCh)

	for {
		select {
		case <-w.pollReqCh:
			// We don't need backoff here because we'll just try again
			// after the pollRate elapses.
			head, err := w.getLatestHeader()
			if err != nil {
				w.lgr.Error("error getting latest header", "err", err)
				reqPollAfter()
				continue
			}
			if w.currHead != nil && w.currHead.Hash() == head.Hash() {
				w.lgr.Trace("no change in head, skipping notifications")
				reqPollAfter()
				continue
			}

			w.lgr.Trace("notifying subscribers of new head", "head", head.Hash())
			w.currHead = head
			w.mtx.RLock()
			for _, sub := range w.subs {
				sub <- head
			}
			w.mtx.RUnlock()
			reqPollAfter()
		case <-w.ctx.Done():
			w.Client.Close()
			return
		}
	}
}

func (w *PollingClient) getLatestHeader() (*types.Header, error) {
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	defer cancel()
	head, err := w.HeaderByNumber(ctx, big.NewInt(rpc.LatestBlockNumber.Int64()))
	if err == nil && head == nil {
		err = ethereum.NotFound
	}
	return head, err
}

func (w *PollingClient) reqPoll() {
	w.pollReqCh <- struct{}{}
}
