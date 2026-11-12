package whatsmeow_service

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/patrickmn/go-cache"
	"go.mau.fi/whatsmeow"
)

type retryFailureRepository struct {
	instance_repository.InstanceRepository
	mu           sync.Mutex
	instance     instance_model.Instance
	reads        int
	writes       int
	repeated     chan struct{}
	stopped      bool
	readFailures int
	failRestart  bool
}

func (r *retryFailureRepository) GetInstanceByID(string) (*instance_model.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.stopped {
		row := r.instance
		row.DisconnectReason = "Stopped by operator"
		return &row, nil
	}
	// Callback reads state successfully; ReconnectClient then fails to reload it.
	if r.readFailures > 0 {
		r.readFailures--
		return nil, errors.New("status database temporarily down")
	}
	if r.failRestart && (r.reads == 2 || r.reads == 3) {
		return nil, errors.New("temporary database outage")
	}
	row := r.instance
	return &row, nil
}
func (r *retryFailureRepository) UpdateConnected(_ string, connected bool, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instance.Connected = connected
	r.instance.DisconnectReason = reason
	r.writes++
	if r.writes == 2 {
		r.stopped = true
		close(r.repeated)
	}
	return nil
}
func TestStartupRetrySurvivesFailedRestart(t *testing.T) {
	id := "retry-start-failure"
	repo := &retryFailureRepository{instance: instance_model.Instance{Id: id, Jid: "5511999999999@s.whatsapp.net"}, repeated: make(chan struct{}), failRestart: true}
	retry := &proxyRetrier{attempts: map[string]int{}, pending: map[string]bool{}, base: time.Millisecond, max: time.Millisecond}
	w := whatsmeowService{instanceRepository: repo, clientPointer: map[string]*whatsmeow.Client{}, myClientPointer: map[string]*MyClient{}, killChannel: map[string]chan bool{}, userInfoCache: cache.New(cache.NoExpiration, time.Minute), config: &config.Config{}, loggerWrapper: logger_wrapper.NewLoggerManager(&config.Config{LogDirectory: t.TempDir()})}
	copy := repo.instance
	w.retryFailedStart(&ClientData{Instance: &copy}, errors.New("startup failed"), startupRetryReason, retry)
	select {
	case <-repo.repeated:
	case <-time.After(time.Second):
		t.Fatal("failed restart discarded the next attempt")
	}
	// Wait until the last timer observes the explicit stop before touching globals.
	deadline := time.Now().Add(time.Second)
	for {
		retry.mu.Lock()
		pending := retry.pending[id]
		retry.mu.Unlock()
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry did not stop after operator changed the state")
		}
		time.Sleep(time.Millisecond)
	}
	clientStopping.Delete(id)
	clientReconnecting.Delete(id)
}

func TestStartupRetryRejectsUnpairedAndManuallyStoppedSessions(t *testing.T) {
	for _, inst := range []*instance_model.Instance{nil, {}, {Jid: "5511999999999@s.whatsapp.net", DisconnectReason: "401: logged out from another device"}, {Jid: "5511999999999@s.whatsapp.net", DisconnectReason: "Stopped by operator"}} {
		if shouldRetryFailedStart(inst, false, startupRetryReason) {
			t.Fatalf("must not restart session: %+v", inst)
		}
	}
	inst := &instance_model.Instance{Jid: "5511999999999@s.whatsapp.net", DisconnectReason: "Reconnecting"}
	if !shouldRetryFailedStart(inst, false, startupRetryReason) {
		t.Fatal("automatic recovery state must qualify")
	}
	if shouldRetryFailedStart(inst, true, startupRetryReason) {
		t.Fatal("must not restart a client that connected meanwhile")
	}
}

func TestStartupRetryKeepsPollingWhileStartOrDatabaseIsUnavailable(t *testing.T) {
	for _, mode := range []string{"starting", "database"} {
		t.Run(mode, func(t *testing.T) {
			id := "retry-wait-" + mode
			repo := &retryFailureRepository{instance: instance_model.Instance{Id: id, Jid: "5511999999999@s.whatsapp.net"}, repeated: make(chan struct{})}
			if mode == "starting" {
				clientStarting.Store(id, struct{}{})
				defer clientStarting.Delete(id)
			} else {
				repo.readFailures = 1000
			}
			retry := &proxyRetrier{attempts: map[string]int{}, pending: map[string]bool{}, base: time.Millisecond, max: time.Millisecond}
			w := whatsmeowService{instanceRepository: repo, clientPointer: map[string]*whatsmeow.Client{}, loggerWrapper: logger_wrapper.NewLoggerManager(&config.Config{LogDirectory: t.TempDir()})}
			copy := repo.instance
			w.retryFailedStart(&ClientData{Instance: &copy}, errors.New("startup failed"), startupRetryReason, retry)
			deadline := time.Now().Add(time.Second)
			for {
				repo.mu.Lock()
				reads := repo.reads
				repo.mu.Unlock()
				if reads >= 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("pending recovery stopped polling")
				}
				time.Sleep(time.Millisecond)
			}
			repo.mu.Lock()
			writes := repo.writes
			repo.stopped = true
			repo.mu.Unlock()
			if writes != 1 {
				t.Fatalf("polling must not overwrite the state: got %d writes", writes)
			}
			for {
				retry.mu.Lock()
				pending := retry.pending[id]
				retry.mu.Unlock()
				if !pending {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("polling ignored manual stop")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
