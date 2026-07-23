package wavonyx

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	rand "math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/phalconyx/wavonyx/internal/registry"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// SessionAPI is the surface the HTTP server depends on. *Manager implements it.
type SessionAPI interface {
	Create(ctx context.Context, id, webhookURL string) (*SessionInfo, error)
	List(ctx context.Context) []SessionInfo
	Get(ctx context.Context, id string) (*SessionInfo, error)
	Login(ctx context.Context, id string) (*QRInfo, error)
	QR(ctx context.Context, id string) (*QRInfo, error)
	Logout(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	Send(ctx context.Context, id string, req SendRequest) (*SendResult, error)
	SendMedia(ctx context.Context, id string, req MediaSendRequest) (*SendResult, error)
	Recent(ctx context.Context, id string, limit int) ([]InboundMessage, error)
	DownloadMedia(ctx context.Context, id, token string) (*MediaContent, error)
}

// webhookSink is the webhook dispatcher as the manager uses it.
type webhookSink interface {
	eventSink
	Close()
}

var _ SessionAPI = (*Manager)(nil)

var sessionIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Manager owns all sessions, the shared SQLite-backed device store and session
// registry, and the webhook dispatcher.
type Manager struct {
	cfg    Config
	store  waStore
	reg    *registry.Registry
	hooks  webhookSink
	log    *slog.Logger
	newRNG func() *rand.Rand

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	sessions map[string]*session
}

// NewManager builds a production Manager: it opens (and migrates) the SQLite
// database under cfg.DataDir with the pure-Go modernc driver, wires the
// whatsmeow store, the session registry, and the webhook dispatcher.
func NewManager(cfg Config) (*Manager, error) {
	cfg = cfg.withDefaults()

	// The device name shown in WhatsApp's Linked Devices list. This is a global
	// in whatsmeow read at pairing time, so it must be set before any Login.
	if cfg.DeviceName != "" {
		store.DeviceProps.Os = proto.String(cfg.DeviceName)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(cfg.DataDir, "wavonyx.db")
	dsn := "file:" + dbPath + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1) // single writer avoids SQLITE_BUSY across store + registry

	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Noop)
	if err := container.Upgrade(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate store: %w", err)
	}

	store := newRealStore(container, waLog.Noop)
	reg := registry.New(db)
	hooks := NewDispatcher(cfg.Webhook, cfg.Logger)

	return newManager(cfg, store, reg, hooks, cfg.Logger, nil), nil
}

// newManager is the dependency-injected constructor used by NewManager and by
// tests (with in-memory fakes).
func newManager(cfg Config, store waStore, reg *registry.Registry, hooks webhookSink, log *slog.Logger, newRNG func() *rand.Rand) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if newRNG == nil {
		newRNG = defaultRNG
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:      cfg,
		store:    store,
		reg:      reg,
		hooks:    hooks,
		log:      log,
		newRNG:   newRNG,
		ctx:      ctx,
		cancel:   cancel,
		sessions: make(map[string]*session),
	}
}

// Start loads the registry and attaches (connects) already-paired sessions.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.reg.Init(m.ctx); err != nil {
		return fmt.Errorf("init registry: %w", err)
	}
	rows, err := m.reg.List(m.ctx)
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}
	jids, err := m.store.LoggedInJIDs(m.ctx)
	if err != nil {
		return fmt.Errorf("load devices: %w", err)
	}
	loggedIn := make(map[string]bool, len(jids))
	for _, j := range jids {
		loggedIn[j] = true
	}

	for _, row := range rows {
		s := m.newSession(row.ID, row.JID, row.WebhookURL, row.CreatedAt)
		m.mu.Lock()
		m.sessions[row.ID] = s
		m.mu.Unlock()
		s.start()

		switch {
		case row.JID == "":
			// never paired: stays created
		case loggedIn[row.JID]:
			if err := s.attach(); err != nil {
				m.log.Warn("attach failed", "session", row.ID, "err", err)
			}
		default:
			s.setStatus(StatusLoggedOut)
		}
	}
	return nil
}

// Close disconnects all sessions, stops the dispatcher, and closes the store.
func (m *Manager) Close() error {
	m.cancel()
	m.mu.Lock()
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.stop()
	}
	if m.hooks != nil {
		m.hooks.Close()
	}
	return m.store.Close()
}

func (m *Manager) newSession(id, jid, webhookURL string, createdAt time.Time) *session {
	return &session{
		id:         id,
		createdAt:  createdAt,
		status:     initialStatus(jid),
		jid:        jid,
		webhookURL: webhookURL,
		unread:     make(map[unreadKey]readTarget),
		ring:       NewRing(m.cfg.RingSize),
		sendCh:     make(chan *sendJob, m.cfg.SendQueue),
		done:       make(chan struct{}),
		rng:        m.newRNG(),
		cfg:        m.cfg,
		store:      m.store,
		reg:        m.reg,
		hooks:      m.hooks,
		log:        m.log,
		mgrCtx:     m.ctx,
	}
}

func initialStatus(jid string) Status {
	if jid == "" {
		return StatusCreated
	}
	return StatusDisconnected
}

func (m *Manager) session(id string) (*session, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

// Create registers a new session. An empty id is auto-generated.
func (m *Manager) Create(ctx context.Context, id, webhookURL string) (*SessionInfo, error) {
	if id == "" {
		id = genSessionID()
	} else if !sessionIDRe.MatchString(id) {
		return nil, ErrInvalidSessionID
	}

	now := time.Now().UTC()
	if err := m.reg.Create(ctx, registry.Session{ID: id, WebhookURL: webhookURL, CreatedAt: now}); err != nil {
		if errors.Is(err, registry.ErrExists) {
			return nil, ErrSessionExists
		}
		return nil, err
	}

	m.mu.Lock()
	if _, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return nil, ErrSessionExists
	}
	s := m.newSession(id, "", webhookURL, now)
	m.sessions[id] = s
	m.mu.Unlock()
	s.start()

	info := s.info()
	return &info, nil
}

func (m *Manager) Get(ctx context.Context, id string) (*SessionInfo, error) {
	s, err := m.session(id)
	if err != nil {
		return nil, err
	}
	info := s.info()
	return &info, nil
}

func (m *Manager) List(ctx context.Context) []SessionInfo {
	m.mu.RLock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.info())
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (m *Manager) Login(ctx context.Context, id string) (*QRInfo, error) {
	s, err := m.session(id)
	if err != nil {
		return nil, err
	}
	return s.login(ctx)
}

func (m *Manager) QR(ctx context.Context, id string) (*QRInfo, error) {
	s, err := m.session(id)
	if err != nil {
		return nil, err
	}
	return s.currentQR()
}

func (m *Manager) Logout(ctx context.Context, id string) error {
	s, err := m.session(id)
	if err != nil {
		return err
	}
	return s.logout(ctx)
}

// Delete tears a session down completely: worker, device, and registry row.
func (m *Manager) Delete(ctx context.Context, id string) error {
	s, err := m.session(id)
	if err != nil {
		return err
	}
	s.stop()
	if jid := s.currentJID(); jid != "" {
		if err := m.store.DeleteDevice(ctx, jid); err != nil {
			m.log.Warn("delete device failed", "session", id, "err", err)
		}
	}
	if err := m.reg.Delete(ctx, id); err != nil && !errors.Is(err, registry.ErrNotFound) {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}

func (m *Manager) Send(ctx context.Context, id string, req SendRequest) (*SendResult, error) {
	s, err := m.session(id)
	if err != nil {
		return nil, err
	}
	return s.send(ctx, req)
}

func (m *Manager) SendMedia(ctx context.Context, id string, req MediaSendRequest) (*SendResult, error) {
	s, err := m.session(id)
	if err != nil {
		return nil, err
	}
	return s.sendMedia(ctx, req)
}

func (m *Manager) Recent(ctx context.Context, id string, limit int) ([]InboundMessage, error) {
	s, err := m.session(id)
	if err != nil {
		return nil, err
	}
	return s.ring.Recent(limit), nil
}

func (m *Manager) DownloadMedia(ctx context.Context, id, token string) (*MediaContent, error) {
	s, err := m.session(id)
	if err != nil {
		return nil, err
	}
	return s.downloadMedia(ctx, token)
}

func genSessionID() string {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return fmt.Sprintf("s_%016x", time.Now().UnixNano())
	}
	return "s_" + hex.EncodeToString(b[:])
}

func defaultRNG() *rand.Rand {
	return rand.New(rand.NewPCG(cryptoSeed(), cryptoSeed()))
}

func cryptoSeed() uint64 {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano())
	}
	return binary.LittleEndian.Uint64(b[:])
}
