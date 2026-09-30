package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/logx"
	"github.com/brbbruno/agent-bridge/internal/model"
)

type Server struct {
	Home    string
	Config  config.Config
	Token   string
	Router  *Router
	Channel channel.Channel
	Logger  *logx.Logger

	mu       sync.Mutex
	cancel   context.CancelFunc
	started  time.Time
	listener net.Listener
}

func NewServer(home string, cfg config.Config, token string, telegram channel.Channel, logger *logx.Logger) (*Server, error) {
	router, err := NewRouter(home, cfg, telegram, logger)
	if err != nil {
		return nil, err
	}
	return &Server{Home: home, Config: cfg, Token: token, Router: router, Channel: telegram, Logger: logger, started: time.Now()}, nil
}

func (s *Server) Address() string { return "127.0.0.1:" + strconv.Itoa(s.Config.Port) }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.authenticate(s.handleHealth))
	mux.HandleFunc("/v1/", s.authenticate(s.handleEvent))
	mux.HandleFunc("/admin/away", s.authenticate(s.handleAway))
	mux.HandleFunc("/admin/status", s.authenticate(s.handleStatus))
	mux.HandleFunc("/admin/test", s.authenticate(s.handleTest))
	mux.HandleFunc("/admin/stop", s.authenticate(s.handleStop))
	mux.HandleFunc("/", s.authenticate(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	return mux
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.Address())
	if err != nil {
		return fmt.Errorf("não foi possível abrir %s: %w", s.Address(), err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.listener = listener
	s.started = time.Now()
	s.mu.Unlock()
	defer cancel()
	if s.Channel != nil {
		go s.pollTelegram(runCtx)
	}
	httpServer := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-runCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
		}
	}()
	err = httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r.Header.Get(tokenHeader), s.Token) {
			http.Error(w, "não autorizado", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	kind := strings.TrimPrefix(r.URL.Path, "/v1/")
	if kind != string(model.EventStop) && kind != string(model.EventPermission) && kind != string(model.EventQuestion) && kind != string(model.EventPrompt) && kind != string(model.EventSessionEnd) {
		http.Error(w, "evento inválido", http.StatusNotFound)
		return
	}
	var event model.Event
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&event); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	if string(event.Type) != kind {
		http.Error(w, "tipo de evento incompatível", http.StatusBadRequest)
		return
	}
	resolution := s.Router.HandleEvent(r.Context(), event)
	writeJSON(w, http.StatusOK, resolution)
}

func (s *Server) handleAway(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Away bool `json:"away"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	s.Router.SetAway(request.Away)
	writeJSON(w, http.StatusOK, map[string]bool{"away": request.Away})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	away, waiting := s.Router.Status()
	writeJSON(w, http.StatusOK, Status{Away: away, Waiting: waiting, StartedAt: s.started, Uptime: time.Since(s.started)})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if s.Channel == nil {
		http.Error(w, "canal não configurado", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := s.Channel.Send(ctx, "Teste do agent-bridge: conexão com o Telegram funcionando.", nil, false); err != nil {
		s.Logger.Errorf("enviar teste Telegram: %v", err)
		http.Error(w, "não foi possível enviar a mensagem de teste", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stopping": true})
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		go cancel()
	}
}

func (s *Server) pollTelegram(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		updates, err := s.Channel.Updates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.Logger.Errorf("receber atualização Telegram: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, update := range updates {
			if update.ID >= offset {
				offset = update.ID + 1
			}
			if !update.Ignored {
				s.Router.HandleUpdate(ctx, update)
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func DecodeEvent(reader io.Reader) (model.Event, error) {
	var event model.Event
	err := json.NewDecoder(reader).Decode(&event)
	return event, err
}
