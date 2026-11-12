package whatsmeow_service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"gorm.io/gorm"
)

const startupRetryReason = "connection retry"

var startupRetry = &proxyRetrier{attempts: map[string]int{}, pending: map[string]bool{}, base: 10 * time.Second, max: 30 * time.Second}

func (w whatsmeowService) retryAfterStartupFailure(cd *ClientData, cause error) {
	w.retryFailedStart(cd, cause, startupRetryReason, startupRetry)
}

func shouldRetryFailedStart(inst *instance_model.Instance, connected bool, reason string) bool {
	return inst != nil && inst.Jid != "" && !inst.Connected && !connected &&
		(strings.HasPrefix(inst.DisconnectReason, reason) || inst.DisconnectReason == "Reconnecting")
}

// A failed setup or restart must leave another attempt scheduled. Only saved
// sessions qualify: QR pairing and an explicit logout must never restart here.
func (w whatsmeowService) retryFailedStart(cd *ClientData, cause error, prefix string, retrier *proxyRetrier) {
	id := cd.Instance.Id
	if cd.Instance.Jid == "" || isRemoteLoggedOut(id) {
		return
	}
	delay, scheduled := retrier.next(id)
	reason := fmt.Sprintf("%s (retrying): %v", prefix, cause)
	if scheduled {
		reason = fmt.Sprintf("%s (retry in %s): %v", prefix, delay, cause)
	}
	cd.Instance.Connected = false
	cd.Instance.DisconnectReason = reason
	w.loggerWrapper.GetLogger(id).LogWarn("[%s] %s", id, reason)
	if err := w.instanceRepository.UpdateConnected(id, false, reason); err != nil {
		w.loggerWrapper.GetLogger(id).LogError("[%s] Failed to record retry: %v", id, err)
	}
	if !scheduled {
		return
	}
	var check func()
	reschedule := func() {
		if nextDelay, ok := retrier.next(id); ok {
			time.AfterFunc(nextDelay, check)
		}
	}
	check = func() {
		retrier.done(id)
		inst, err := w.instanceRepository.GetInstanceByID(id)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				retrier.reset(id)
				return
			}
			// A status read failure must not overwrite a newer manual stop/logout.
			w.loggerWrapper.GetLogger(id).LogWarn("[%s] Retry state temporarily unavailable: %v", id, err)
			reschedule()
			return
		}
		client := w.clientPointer[id]
		if isRemoteLoggedOut(id) || !shouldRetryFailedStart(inst, client != nil && client.IsConnected(), prefix) {
			retrier.reset(id)
			return
		}
		_, restarting := clientReconnecting.Load(id)
		if ClientStarting(id) || restarting {
			reschedule()
			return
		}
		w.loggerWrapper.GetLogger(id).LogInfo("[%s] Retrying saved session after %s", id, prefix)
		if err := w.ReconnectClient(id); err != nil {
			w.retryFailedStart(&ClientData{Instance: inst}, fmt.Errorf("restart saved session: %w", err), prefix, retrier)
		}
	}
	time.AfterFunc(delay, check)
}
