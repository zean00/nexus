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
	if sessionID == "" {
		return closedWebChatHubChannel()
	}
	return h.subscribe("session:" + sessionID)
}

func (h *WebChatSessionHub) SubscribeUser(tenantID, userID string) chan struct{} {
	if tenantID == "" || userID == "" {
		return closedWebChatHubChannel()
	}
	return h.subscribe("user:" + tenantID + ":" + userID)
}

func (h *WebChatSessionHub) subscribe(topic string) chan struct{} {
	ch := make(chan struct{}, 1)
	if h == nil || topic == "" {
		close(ch)
		return ch
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[topic] == nil {
		h.subs[topic] = map[chan struct{}]struct{}{}
	}
	h.subs[topic][ch] = struct{}{}
	return ch
}

func (h *WebChatSessionHub) Unsubscribe(sessionID string, ch chan struct{}) {
	if sessionID == "" {
		return
	}
	h.unsubscribe("session:"+sessionID, ch)
}

func (h *WebChatSessionHub) UnsubscribeUser(tenantID, userID string, ch chan struct{}) {
	if tenantID == "" || userID == "" {
		return
	}
	h.unsubscribe("user:"+tenantID+":"+userID, ch)
}

func (h *WebChatSessionHub) unsubscribe(topic string, ch chan struct{}) {
	if h == nil || topic == "" || ch == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if sessionSubs := h.subs[topic]; sessionSubs != nil {
		delete(sessionSubs, ch)
		if len(sessionSubs) == 0 {
			delete(h.subs, topic)
		}
	}
	close(ch)
}

func (h *WebChatSessionHub) Notify(sessionID string) {
	if sessionID == "" {
		return
	}
	h.notify("session:" + sessionID)
}

func (h *WebChatSessionHub) NotifyUser(tenantID, userID string) {
	if tenantID == "" || userID == "" {
		return
	}
	h.notify("user:" + tenantID + ":" + userID)
}

func (h *WebChatSessionHub) notify(topic string) {
	if h == nil || topic == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[topic] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func closedWebChatHubChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
