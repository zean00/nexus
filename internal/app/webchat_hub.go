package app

import "sync"

type WebChatSessionHub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func NewWebChatSessionHub() *WebChatSessionHub {
	return &WebChatSessionHub{subs: map[string]map[chan struct{}]struct{}{}}
}

func (h *WebChatSessionHub) Subscribe(sessionID string) chan struct{} {
	ch := make(chan struct{}, 1)
	if h == nil || sessionID == "" {
		close(ch)
		return ch
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[sessionID] == nil {
		h.subs[sessionID] = map[chan struct{}]struct{}{}
	}
	h.subs[sessionID][ch] = struct{}{}
	return ch
}

func (h *WebChatSessionHub) Unsubscribe(sessionID string, ch chan struct{}) {
	if h == nil || sessionID == "" || ch == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if sessionSubs := h.subs[sessionID]; sessionSubs != nil {
		delete(sessionSubs, ch)
		if len(sessionSubs) == 0 {
			delete(h.subs, sessionID)
		}
	}
	close(ch)
}

func (h *WebChatSessionHub) Notify(sessionID string) {
	if h == nil || sessionID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[sessionID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
