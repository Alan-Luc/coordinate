package coordinator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const communicationRetention = 7 * 24 * time.Hour

func communicationMessageBlocksForget(message CommunicationMessage) bool {
	return message.Kind != "result" && message.ResolvedAt == 0 || message.Kind == "result" && message.AcknowledgedAt == 0
}

func communicationMessageBlocksRelease(message CommunicationMessage) bool {
	return message.Kind != "result" && message.ResolvedAt == 0
}

func eligibleCommunicationEviction(message CommunicationMessage) bool {
	return !communicationMessageBlocksForget(message)
}

func communicationEvictionTime(message CommunicationMessage) int64 {
	if message.Kind == "result" && message.AcknowledgedAt != 0 {
		return message.AcknowledgedAt
	}
	if message.ResolvedAt != 0 {
		return message.ResolvedAt
	}
	return 0
}

func pruneExpiredCommunicationMessages(state *communicationState, now time.Time) bool {
	cutoff := now.Add(-communicationRetention).UnixMilli()
	kept := state.Messages[:0]
	for _, message := range state.Messages {
		if eligibleCommunicationEviction(message) && communicationEvictionTime(message) <= cutoff {
			continue
		}
		kept = append(kept, message)
	}
	changed := len(kept) != len(state.Messages)
	state.Messages = kept
	if changed {
		pruneCommunicationSeen(state)
	}
	return changed
}

func pruneCommunicationSeen(state *communicationState) {
	retained := map[string]bool{}
	for _, message := range state.Messages {
		retained[message.MessageID] = true
	}
	seen := state.Seen[:0]
	for _, entry := range state.Seen {
		if retained[entry.MessageID] {
			seen = append(seen, entry)
		}
	}
	state.Seen = seen
}

func visibleCommunicationMessages(messages []CommunicationMessage, now time.Time) []CommunicationMessage {
	cutoff := now.Add(-communicationRetention).UnixMilli()
	visible := make([]CommunicationMessage, 0, len(messages))
	for _, message := range messages {
		if eligibleCommunicationEviction(message) && communicationEvictionTime(message) <= cutoff {
			continue
		}
		visible = append(visible, message)
	}
	return visible
}

func communicationDocumentFits(state communicationState) bool {
	encoded, err := json.Marshal(state)
	// Reserve per-message delivery metadata and per-claim receipt space before accepting work.
	reserve := 4096 + 1024*len(state.Messages)
	for _, message := range state.Messages {
		reserve += 512 * len(message.Replies)
	}
	return err == nil && len(encoded)+1+reserve <= MaxCommunicationBytes
}

func communicationDocumentWithinLimit(state communicationState) bool {
	encoded, err := json.Marshal(state)
	return err == nil && len(encoded)+1 <= MaxCommunicationBytes
}

func communicationReplyFits(state communicationState) bool {
	encoded, err := json.Marshal(state)
	reserve := 0
	for _, message := range state.Messages {
		for _, reply := range message.Replies {
			if reply.Receipt == nil && reply.Failure == nil {
				reserve += 512
			}
		}
	}
	return err == nil && len(encoded)+1+reserve <= MaxCommunicationBytes
}

func reserveCommunicationReply(state *communicationState, now time.Time) bool {
	candidate := *state
	candidate.Messages = append([]CommunicationMessage{}, state.Messages...)
	candidate.Seen = append([]communicationSeen{}, state.Seen...)
	pruneCommunicationMessages(&candidate, now, communicationReplyFits)
	if !communicationReplyFits(candidate) {
		return false
	}
	*state = candidate
	return true
}

func reserveCommunicationMessage(state *communicationState, message CommunicationMessage, now time.Time) bool {
	candidate := *state
	candidate.Seen = append([]communicationSeen{}, state.Seen...)
	candidate.Messages = append(append([]CommunicationMessage(nil), state.Messages...), message)
	pruneForCommunicationCapacity(&candidate, now)
	if len(candidate.Messages) > 128 || !communicationDocumentFits(candidate) {
		state.Degraded = true
		return false
	}
	candidate.Degraded = false
	*state = candidate
	return true
}

func pruneForCommunicationCapacity(state *communicationState, now time.Time) {
	pruneCommunicationMessages(state, now, communicationDocumentFits)
}

func pruneCommunicationMessages(state *communicationState, now time.Time, fits func(communicationState) bool) {
	pruneExpiredCommunicationMessages(state, now)
	for len(state.Messages) > 128 || !fits(*state) {
		oldest := -1
		for index, message := range state.Messages {
			if eligibleCommunicationEviction(message) && (oldest < 0 || communicationEvictionTime(message) < communicationEvictionTime(state.Messages[oldest])) {
				oldest = index
			}
		}
		if oldest < 0 {
			return
		}
		state.Messages = append(state.Messages[:oldest], state.Messages[oldest+1:]...)
	}
	pruneCommunicationSeen(state)
}

func (c *Coordinator) reapCommunicationTemps() error {
	directory := filepath.Join(c.store.root, "communication")
	if err := checkPrivateDir(directory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".write-") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		file, err := privateFile(path, unix.O_RDONLY)
		if err != nil {
			return errors.New("communication temporary file is not owned and private")
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncDir(directory)
	}
	return nil
}

func managerLeaseEnded(m CommunicationManager) bool {
	if m.Lease == nil {
		return true
	}
	status, err := compareProcess(m.Lease.Process)
	return err == nil && status == ProcessEnded
}

func pruneInactiveManagers(s *communicationState) bool {
	removed := map[string]bool{}
	for _, m := range s.Managers {
		if !m.Enabled || managerLeaseEnded(m) {
			removed[m.ID] = true
		}
	}
	for _, message := range s.Messages {
		if communicationMessageBlocksRelease(message) {
			delete(removed, message.ManagerID)
		}
	}
	if len(removed) == 0 {
		return false
	}
	managers := []CommunicationManager{}
	for _, m := range s.Managers {
		if !removed[m.ID] {
			managers = append(managers, m)
		}
	}
	bindings := []CommunicationBinding{}
	for _, b := range s.Bindings {
		if !removed[b.ManagerID] {
			bindings = append(bindings, b)
		}
	}
	messages := []CommunicationMessage{}
	kept := map[string]bool{}
	for _, message := range s.Messages {
		if !removed[message.ManagerID] {
			messages = append(messages, message)
			kept[message.MessageID] = true
		}
	}
	seen := []communicationSeen{}
	for _, entry := range s.Seen {
		if !removed[entry.ManagerID] && kept[entry.MessageID] {
			seen = append(seen, entry)
		}
	}
	s.Managers, s.Bindings, s.Messages, s.Seen = managers, bindings, messages, seen
	return true
}

func rebindWorkerFromEndedManager(s *communicationState, jobID, managerID string) (liveOwner CommunicationManager, found, rebound bool) {
	for i, b := range s.Bindings {
		if b.JobID != jobID {
			continue
		}
		for _, owner := range s.Managers {
			if owner.ID == b.ManagerID && !managerLeaseEnded(owner) {
				return owner, true, false
			}
		}
		s.Bindings[i].ManagerID = managerID
		moved := map[string]bool{}
		for j := range s.Messages {
			if s.Messages[j].Source.ID == jobID && s.Messages[j].ManagerID == b.ManagerID {
				s.Messages[j].ManagerID = managerID
				s.Messages[j].AttemptGeneration = ""
				moved[s.Messages[j].MessageID] = true
			}
		}
		seen := s.Seen[:0]
		for _, entry := range s.Seen {
			if !moved[entry.MessageID] {
				seen = append(seen, entry)
			}
		}
		s.Seen = seen
		return CommunicationManager{}, true, true
	}
	return CommunicationManager{}, false, false
}
