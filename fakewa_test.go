package wavonyx

import (
	"context"
	"sync"
	"time"
)

// fakeStore is an in-memory waStore. Clients it creates are pre-loaded with the
// configured QR script and pairing JID.
type fakeStore struct {
	mu           sync.Mutex
	paired       map[string]bool
	script       []qrItem
	pairJID      string
	pairPush     string
	newClientErr error
	lastClient   *fakeClient
}

func newFakeStore() *fakeStore { return &fakeStore{paired: map[string]bool{}} }

func (f *fakeStore) NewClient() (waClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.newClientErr != nil {
		return nil, f.newClientErr
	}
	c := newFakeClient(f)
	c.scripted = f.script
	c.pairJID = f.pairJID
	c.pairPush = f.pairPush
	f.lastClient = c
	return c, nil
}

func (f *fakeStore) LoadClient(ctx context.Context, jid string) (waClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.paired[jid] {
		return nil, ErrSessionNotFound
	}
	c := newFakeClient(f)
	c.jid = jid
	c.loggedIn = true
	f.lastClient = c
	return c, nil
}

func (f *fakeStore) DeleteDevice(ctx context.Context, jid string) error {
	f.mu.Lock()
	delete(f.paired, jid)
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) LoggedInJIDs(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.paired))
	for j := range f.paired {
		out = append(out, j)
	}
	return out, nil
}

func (f *fakeStore) Close() error { return nil }

func (f *fakeStore) markPaired(jid string) {
	f.mu.Lock()
	f.paired[jid] = true
	f.mu.Unlock()
}

func (f *fakeStore) client() *fakeClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastClient
}

// fakeClient is an in-memory waClient recording the calls made against it.
type fakeClient struct {
	store *fakeStore

	mu        sync.Mutex
	handlers  waHandlers
	jid       string
	pushName  string
	connected bool
	loggedIn  bool

	scripted []qrItem
	pairJID  string
	pairPush string

	calls      []string // "composing" | "paused" | "send:<text>" | "available"
	sendErr    error
	sendResult waSendResult

	sendEntered chan string   // if set, receives the text when SendText is entered
	sendGate    chan struct{} // if set, SendText blocks until it is closed/received
}

func newFakeClient(store *fakeStore) *fakeClient {
	return &fakeClient{
		store:      store,
		sendResult: waSendResult{ID: "FAKEID", Timestamp: time.Unix(1_700_000_000, 0).UTC()},
	}
}

func (c *fakeClient) SetHandlers(h waHandlers) {
	c.mu.Lock()
	c.handlers = h
	c.mu.Unlock()
}

func (c *fakeClient) PairQR(ctx context.Context) (<-chan qrItem, error) {
	c.mu.Lock()
	scripted := c.scripted
	c.mu.Unlock()
	ch := make(chan qrItem, len(scripted)+1)
	go func() {
		defer close(ch)
		for _, it := range scripted {
			if it.Event == qrEventSuccess {
				c.mu.Lock()
				c.jid = c.pairJID
				c.pushName = c.pairPush
				c.loggedIn = true
				c.connected = true
				store, jid := c.store, c.pairJID
				c.mu.Unlock()
				if store != nil && jid != "" {
					store.markPaired(jid)
				}
			}
			select {
			case <-ctx.Done():
				return
			case ch <- it:
			}
			time.Sleep(time.Millisecond) // let the consumer observe each item
		}
	}()
	return ch, nil
}

func (c *fakeClient) Connect() error {
	c.mu.Lock()
	c.connected = true
	loggedIn := c.loggedIn
	h := c.handlers
	c.mu.Unlock()
	if loggedIn && h.OnConnected != nil {
		h.OnConnected()
	}
	return nil
}

func (c *fakeClient) Disconnect() {
	c.mu.Lock()
	c.connected = false
	h := c.handlers
	c.mu.Unlock()
	if h.OnDisconnected != nil {
		h.OnDisconnected()
	}
}

func (c *fakeClient) Logout(ctx context.Context) error {
	c.mu.Lock()
	c.loggedIn = false
	c.connected = false
	store, jid := c.store, c.jid
	c.mu.Unlock()
	if store != nil && jid != "" {
		_ = store.DeleteDevice(ctx, jid)
	}
	return nil
}

func (c *fakeClient) IsConnected() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.connected }
func (c *fakeClient) IsLoggedIn() bool  { c.mu.Lock(); defer c.mu.Unlock(); return c.loggedIn }
func (c *fakeClient) JID() string       { c.mu.Lock(); defer c.mu.Unlock(); return c.jid }
func (c *fakeClient) PushName() string  { c.mu.Lock(); defer c.mu.Unlock(); return c.pushName }

func (c *fakeClient) SendText(ctx context.Context, jid, text string) (waSendResult, error) {
	c.mu.Lock()
	c.calls = append(c.calls, "send:"+text)
	entered, gate := c.sendEntered, c.sendGate
	err, res := c.sendErr, c.sendResult
	c.mu.Unlock()
	if entered != nil {
		entered <- text
	}
	if gate != nil {
		<-gate
	}
	if err != nil {
		return waSendResult{}, err
	}
	return res, nil
}

func (c *fakeClient) SendAvailable(ctx context.Context) error {
	c.mu.Lock()
	c.calls = append(c.calls, "available")
	c.mu.Unlock()
	return nil
}

func (c *fakeClient) SetComposing(ctx context.Context, jid string, on bool) error {
	c.mu.Lock()
	if on {
		c.calls = append(c.calls, "composing")
	} else {
		c.calls = append(c.calls, "paused")
	}
	c.mu.Unlock()
	return nil
}

func (c *fakeClient) MarkRead(ctx context.Context, ids []string, ts time.Time, chat, sender string) error {
	c.mu.Lock()
	c.calls = append(c.calls, "read:"+chat)
	c.mu.Unlock()
	return nil
}

// --- test helpers ---

func (c *fakeClient) fireMessage(m InboundMessage) {
	c.mu.Lock()
	h := c.handlers
	c.mu.Unlock()
	if h.OnMessage != nil {
		h.OnMessage(m)
	}
}

func (c *fakeClient) fireLoggedOut() {
	c.mu.Lock()
	h := c.handlers
	c.mu.Unlock()
	if h.OnLoggedOut != nil {
		h.OnLoggedOut()
	}
}

func (c *fakeClient) setGate(entered chan string, gate chan struct{}) {
	c.mu.Lock()
	c.sendEntered = entered
	c.sendGate = gate
	c.mu.Unlock()
}

// actions returns recorded calls excluding the async presence "available", so
// send-ordering assertions aren't perturbed by it.
func (c *fakeClient) actions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.calls))
	for _, call := range c.calls {
		if call != "available" {
			out = append(out, call)
		}
	}
	return out
}

func (c *fakeClient) resetCalls() {
	c.mu.Lock()
	c.calls = nil
	c.mu.Unlock()
}
