package app

import (
	"os/exec"
	"strings"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/transcript"
	"github.com/sid/harness/internal/workspace"
)

// Attachment is an image attached to a message (Data is base64 on the wire).
type Attachment struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Data      []byte `json:"data"`
}

// SendMessage is the Send payload. Delivery: "send" (default), "queue" or "steer".
type SendMessage struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
	Delivery    string       `json:"delivery"`
}

// RespondOpts is the Respond payload. Behavior: allow | allow_always | deny.
type RespondOpts struct {
	Behavior string `json:"behavior"`
	Message  string `json:"message"`
}

// ModelSwitch is the SetModel/CycleModel result. ForkedSessionID is set when
// the switch crossed providers and a new session was forked.
type ModelSwitch struct {
	ForkedSessionID string `json:"forkedSessionId,omitempty"`
}

// ModelInfo is one ListModels entry.
type ModelInfo struct {
	Key       string `json:"key"`
	Provider  string `json:"provider"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	Favourite bool   `json:"favourite"`
}

func (s *SessionService) live(id string) (*sessRun, workspace.Session, error) {
	sess, err := s.sessions.Get(id)
	if err != nil {
		return nil, sess, wrapErr(err)
	}
	if sess.ArchivedAt != nil {
		return nil, sess, wrapErr(workspace.ErrArchived)
	}
	sr := s.sup.get(id)
	if err := sr.load(); err != nil {
		return nil, sess, wrapErr(err)
	}
	return sr, sess, nil
}

// Send delivers a user message, launching (or resuming) the agent host when
// needed. While the run is busy: "queue" holds the message in Go until the
// run_result; "steer"/"send" go straight to the CLI (Cursor cannot take
// mid-turn input, so everything is held for it).
func (s *SessionService) Send(sessionID string, m SendMessage) error {
	if strings.TrimSpace(m.Text) == "" && len(m.Attachments) == 0 {
		return badArg("empty message")
	}
	switch m.Delivery {
	case "", "send", "queue", "steer":
	default:
		return badArg("delivery must be send, queue or steer")
	}
	sr, sess, err := s.live(sessionID)
	if err != nil {
		return err
	}
	return wrapErr(s.sendTo(sr, sess.Provider, toUserMessage(m), m.Delivery))
}

func toUserMessage(m SendMessage) agent.UserMessage {
	um := agent.UserMessage{Text: m.Text}
	for _, a := range m.Attachments {
		um.Images = append(um.Images, agent.Image{MediaType: a.MediaType, Name: a.Name, Data: a.Data})
	}
	return um
}

func (s *SessionService) sendTo(sr *sessRun, provider string, um agent.UserMessage, delivery string) error {
	sr.mu.Lock()
	busy := sr.st.busy || len(sr.st.perms) > 0
	if busy && (delivery == "queue" || provider == "cursor") {
		sr.queue = append(sr.queue, um)
		sr.mu.Unlock()
		return nil
	}
	sr.st.busy, sr.st.errored = true, false
	ns, changed := sr.recalcLocked()
	sr.mu.Unlock()
	if changed {
		sr.statusChanged(ns)
	}
	return sr.deliver(um)
}

// Respond answers a permission request or plan proposal.
func (s *SessionService) Respond(sessionID, reqID string, o RespondOpts) error {
	var b agent.Behavior
	switch o.Behavior {
	case "allow":
		b = agent.BehaviorAllow
	case "allow_always":
		b = agent.BehaviorAllowAlways
	case "deny":
		b = agent.BehaviorDeny
	default:
		return badArg("behavior must be allow, allow_always or deny")
	}
	sr, _, err := s.live(sessionID)
	if err != nil {
		return err
	}
	c := sr.liveClient()
	if c == nil {
		return wrapErr(errNoRun)
	}
	return wrapErr(c.Permission(reqID, agent.Decision{Behavior: b, Message: o.Message}))
}

// Interrupt stops the current turn and drops any held messages.
func (s *SessionService) Interrupt(sessionID string) error {
	sr, _, err := s.live(sessionID)
	if err != nil {
		return err
	}
	sr.mu.Lock()
	sr.queue = nil
	sr.mu.Unlock()
	c := sr.liveClient()
	if c == nil {
		return nil
	}
	return wrapErr(c.Interrupt())
}

// Transcript returns the session's blocks (rebuilt from the run logs when the
// session is not live) and makes sure the `blocks/<id>` channel is open.
func (s *SessionService) Transcript(sessionID string) ([]transcript.Block, error) {
	if _, err := s.sessions.Get(sessionID); err != nil {
		return nil, wrapErr(err)
	}
	sr := s.sup.get(sessionID)
	if err := sr.load(); err != nil {
		return nil, wrapErr(err)
	}
	s.sup.out.Ensure("blocks/" + sessionID)
	return sr.b.Snapshot(), nil
}

// ---- models ----

func (s *SessionService) syncFavourites() {
	if s.settings == nil {
		return
	}
	v := s.settings.GetSettings().Values
	var raw any
	if m, ok := v["models"].(map[string]any); ok {
		raw = m["favourites"]
	}
	if raw == nil {
		raw = v["models.favourites"]
	}
	var keys []string
	if l, ok := raw.([]any); ok {
		for _, x := range l {
			if k, ok := x.(string); ok {
				keys = append(keys, k)
			}
		}
	}
	s.models.SetFavourites(keys)
}

// ListModels lists every model of every provider with favourite flags.
func (s *SessionService) ListModels() []ModelInfo {
	s.syncFavourites()
	fav := map[string]bool{}
	for _, m := range s.models.Favourites() {
		fav[m.Key()] = true
	}
	out := []ModelInfo{}
	for _, m := range s.models.All() {
		label := m.Name
		if label == "" {
			label = m.ID
		}
		out = append(out, ModelInfo{Key: m.Key(), Provider: m.Provider, ID: m.ID, Label: label, Favourite: fav[m.Key()]})
	}
	return out
}

func sessionModelKey(sess workspace.Session) string {
	return sess.Provider + "/" + localModelID(sess.Provider, sess.Model)
}

// SetModel switches the model. Within a provider it is live (client.SetModel
// when a host is alive) and persisted for the next launch. Across providers it
// forks a new session in the same worktree, seeds it with a handoff message
// and returns {forkedSessionId}; the source session goes idle.
func (s *SessionService) SetModel(sessionID, modelKey string) (ModelSwitch, error) {
	sr, sess, err := s.live(sessionID)
	if err != nil {
		return ModelSwitch{}, err
	}
	target := agent.ModelProvider(modelKey)
	if target == "" {
		return ModelSwitch{}, badArg("model key must be <provider>/<id>")
	}
	cur := sessionModelKey(sess)
	if s.models.SameProvider(cur, modelKey) {
		if err := s.setCol(sessionID, "model", modelKey); err != nil {
			return ModelSwitch{}, err
		}
		if c := sr.liveClient(); c != nil {
			_ = c.SetModel(agent.ModelLocalID(modelKey))
		}
		s.push()
		return ModelSwitch{}, nil
	}
	id, err := s.fork(sr, sess, modelKey)
	return ModelSwitch{ForkedSessionID: id}, err
}

// CycleModel moves to the next (+1) or previous (-1) favourite.
func (s *SessionService) CycleModel(sessionID string, dir int) (ModelSwitch, error) {
	sess, err := s.sessions.Get(sessionID)
	if err != nil {
		return ModelSwitch{}, wrapErr(err)
	}
	s.syncFavourites()
	m, ok := s.models.Cycle(sessionModelKey(sess), dir)
	if !ok {
		return ModelSwitch{}, &AppError{Code: CodeNotFound, Message: "no models available"}
	}
	return s.SetModel(sessionID, m.Key())
}

func (s *SessionService) fork(src *sessRun, sess workspace.Session, modelKey string) (string, error) {
	sc, err := s.sessions.Scope(sess.ID)
	if err != nil {
		return "", wrapErr(err)
	}
	provider := agent.ModelProvider(modelKey)
	evs, _ := s.sup.allEvents(sess.ID)
	stat := gitOut(sc.Root, "diff", "--stat", "HEAD")
	diff := gitOut(sc.Root, "diff", "HEAD")
	msg := agent.BuildHandoff(evs, stat, diff, 0)

	fork, err := s.sessions.Create(sess.ProjectID, workspace.CreateOpts{
		Title: sess.Title, Provider: provider, Model: modelKey, Mode: defaultMode(provider),
		Effort: "", ScopeKind: workspace.ScopeMain, ForkedFrom: sess.ID,
	})
	if err != nil {
		return "", wrapErr(err)
	}
	// Reuse the source's checkout: no new worktree.
	if _, err := s.db.Exec(`UPDATE sessions SET scope_kind = ?, worktree_path = ?, branch = ?, base_ref = ? WHERE id = ?`,
		sess.ScopeKind, sess.WorktreePath, sess.Branch, sess.BaseRef, fork.ID); err != nil {
		return "", wrapErr(err)
	}
	// Source goes idle.
	if c := src.liveClient(); c != nil {
		_ = c.Interrupt()
	}
	src.mu.Lock()
	src.queue = nil
	src.st.busy, src.st.errored = false, false
	src.st.perms = map[string]bool{}
	ns, changed := src.recalcLocked()
	src.mu.Unlock()
	if changed {
		src.statusChanged(ns)
	} else {
		_ = s.sessions.SetStatus(sess.ID, StatusIdle)
	}

	fsr := s.sup.get(fork.ID)
	if err := fsr.load(); err != nil {
		return fork.ID, wrapErr(err)
	}
	s.push()
	// A failed launch shows up as an error block/status on the fork.
	_ = s.sendTo(fsr, provider, agent.UserMessage{Text: msg}, "send")
	return fork.ID, nil
}

func gitOut(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	b, _ := cmd.Output()
	return string(b)
}

// ---- mode / effort ----

// SetMode sets the permission mode (native or display name); applied live
// when a host is running and persisted for the next launch.
func (s *SessionService) SetMode(sessionID, mode string) error {
	sr, sess, err := s.live(sessionID)
	if err != nil {
		return err
	}
	native, ok := normalizeMode(sess.Provider, mode)
	if !ok {
		return badArg("unknown mode " + mode)
	}
	return s.applyMode(sr, sessionID, native)
}

func (s *SessionService) applyMode(sr *sessRun, id, native string) error {
	if err := s.setCol(id, "mode", native); err != nil {
		return err
	}
	if c := sr.liveClient(); c != nil {
		_ = c.SetMode(native)
	}
	s.push()
	return nil
}

// CycleMode advances the mode: agent→plan→ask (cursor), default→acceptEdits→plan (claude).
func (s *SessionService) CycleMode(sessionID string) error {
	sr, sess, err := s.live(sessionID)
	if err != nil {
		return err
	}
	next, ok := nextMode(sess.Provider, sess.Mode)
	if !ok {
		return badArg("provider has no modes: " + sess.Provider)
	}
	return s.applyMode(sr, sessionID, next)
}

// SetEffort stores the effort; it applies at the next host launch.
func (s *SessionService) SetEffort(sessionID, effort string) error {
	if _, err := s.sessions.Get(sessionID); err != nil {
		return wrapErr(err)
	}
	err := s.setCol(sessionID, "effort", effort)
	s.push()
	return err
}

func (s *SessionService) setCol(id, col, v string) error {
	switch col {
	case "model", "mode", "effort":
	default:
		return badArg("bad column")
	}
	_, err := s.db.Exec("UPDATE sessions SET "+col+" = ?, updated = strftime('%s','now')*1000 WHERE id = ?", v, id)
	return wrapErr(err)
}
