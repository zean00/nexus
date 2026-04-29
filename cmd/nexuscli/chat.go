package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	tui "github.com/grindlemire/go-tui"
)

type chatItem struct {
	ID      string       `json:"id"`
	Type    string       `json:"type"`
	Role    string       `json:"role"`
	Text    string       `json:"text"`
	Status  string       `json:"status"`
	AwaitID string       `json:"await_id"`
	Choices []chatChoice `json:"choices"`
	Partial bool         `json:"partial"`
}

type chatChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type chatPayload struct {
	Items    []chatItem    `json:"items"`
	Activity *chatActivity `json:"activity"`
}

type chatActivity struct {
	Phase string `json:"phase"`
}

type chatUpdate struct {
	items  []chatItem
	status string
	err    error
}

type chatClient struct {
	state cliState
}

func chat(args []string) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	dev := fs.Bool("dev-login", false, "create a dev webchat session before opening the console")
	baseURL := fs.String("base-url", "http://localhost:8080", "gateway base URL for --dev-login")
	email := fs.String("email", "dev@example.com", "webchat email identity for --dev-login")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dev {
		if err := devLogin([]string{"--base-url", *baseURL, "--email", *email}); err != nil {
			return err
		}
	}
	state, err := loadState()
	if err != nil {
		return err
	}
	client := chatClient{state: state}
	component := newChatComponent(client)
	app, err := tui.NewApp(
		tui.WithRootComponent(component),
		tui.WithMouse(),
		tui.WithGlobalKeyHandler(func(ke tui.KeyEvent) bool {
			if ke.Is(tui.KeyRune, tui.ModCtrl) && ke.Rune == 'c' {
				ke.App().Stop()
				return true
			}
			return false
		}),
	)
	if err != nil {
		return err
	}
	defer app.Close()
	return app.Run()
}

type chatComponent struct {
	client  chatClient
	input   *tui.Input
	items   *tui.State[[]chatItem]
	status  *tui.State[string]
	updates chan chatUpdate
	cancel  context.CancelFunc
	app     *tui.App
}

func newChatComponent(client chatClient) *chatComponent {
	c := &chatComponent{
		client:  client,
		items:   tui.NewState([]chatItem{}),
		status:  tui.NewState("Connecting..."),
		updates: make(chan chatUpdate, 32),
	}
	c.input = tui.NewInput(
		tui.WithInputAutoFocus(true),
		tui.WithInputBorder(tui.BorderRounded),
		tui.WithInputFocusColor(tui.Cyan),
		tui.WithInputPlaceholder("Message Nexus. /new starts a chat, /respond <await_id> <reply> answers an await."),
		tui.WithInputOnSubmit(c.submit),
	)
	return c
}

func (c *chatComponent) Init() func() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.stream(ctx)
	return cancel
}

func (c *chatComponent) KeyMap() tui.KeyMap {
	return tui.KeyMap{
		tui.OnPreemptStop(tui.KeyCtrlC, func(ke tui.KeyEvent) {
			ke.App().Stop()
		}),
	}
}

func (c *chatComponent) Watchers() []tui.Watcher {
	return []tui.Watcher{
		tui.NewChannelWatcher(c.updates, func(update chatUpdate) {
			if update.err != nil {
				c.status.Set("Error: " + update.err.Error())
				return
			}
			if update.items != nil {
				c.items.Set(update.items)
			}
			if update.status != "" {
				c.status.Set(update.status)
			}
		}),
	}
}

func (c *chatComponent) Render(app *tui.App) *tui.Element {
	c.app = app
	root := tui.New(
		tui.WithDirection(tui.Column),
		tui.WithPadding(1),
		tui.WithGap(1),
	)
	header := tui.New(
		tui.WithBorder(tui.BorderSingle),
		tui.WithPaddingTRBL(0, 1, 0, 1),
		tui.WithText("Nexus Console"),
		tui.WithTextStyle(tui.NewStyle().Bold().Foreground(tui.Cyan)),
		tui.WithHeight(3),
	)
	status := tui.New(
		tui.WithText(c.status.Get()),
		tui.WithTextStyle(tui.NewStyle().Dim()),
		tui.WithHeight(1),
	)
	transcript := tui.New(
		tui.WithDirection(tui.Column),
		tui.WithFlexGrow(1),
		tui.WithScrollable(tui.ScrollVertical),
		tui.WithBorder(tui.BorderSingle),
		tui.WithPadding(1),
		tui.WithGap(1),
	)
	for _, item := range c.visibleItems() {
		transcript.AddChild(renderChatItem(item))
	}
	input := app.Mount(c, 0, func() tui.Component { return c.input })
	root.AddChild(header, status, transcript, input)
	return root
}

func (c *chatComponent) visibleItems() []chatItem {
	items := c.items.Get()
	if len(items) <= 80 {
		return items
	}
	return items[len(items)-80:]
}

func renderChatItem(item chatItem) *tui.Element {
	title := item.Role
	if title == "" {
		title = item.Type
	}
	if item.Status != "" && item.Status != "completed" {
		title += " [" + item.Status + "]"
	}
	style := tui.NewStyle()
	switch item.Role {
	case "assistant":
		style = style.Foreground(tui.Green)
	case "user":
		style = style.Foreground(tui.Cyan)
	default:
		style = style.Foreground(tui.Yellow)
	}
	box := tui.New(tui.WithDirection(tui.Column), tui.WithGap(1))
	box.AddChild(tui.New(tui.WithText(title), tui.WithTextStyle(style.Bold())))
	text := strings.TrimSpace(item.Text)
	if text == "" && item.AwaitID != "" {
		text = "Awaiting response: " + item.AwaitID
	}
	if text != "" {
		box.AddChild(tui.New(tui.WithText(text), tui.WithWrap(true)))
	}
	if len(item.Choices) > 0 {
		labels := make([]string, 0, len(item.Choices))
		for _, choice := range item.Choices {
			labels = append(labels, choice.Label)
		}
		box.AddChild(tui.New(tui.WithText("Choices: "+strings.Join(labels, ", ")), tui.WithTextStyle(tui.NewStyle().Dim())))
	}
	return box
}

func (c *chatComponent) submit(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	c.input.Clear()
	c.status.Set("Sending...")
	go func() {
		var err error
		switch {
		case text == "/quit" || text == "/exit":
			c.status.Set("Closing...")
			if c.cancel != nil {
				c.cancel()
			}
			if c.app != nil {
				c.app.Stop()
			}
			return
		case text == "/new":
			err = c.client.newChat(context.Background())
		case strings.HasPrefix(text, "/respond "):
			err = c.client.respond(context.Background(), strings.TrimSpace(strings.TrimPrefix(text, "/respond ")))
		default:
			err = c.client.send(context.Background(), text)
		}
		if err != nil {
			c.updates <- chatUpdate{err: err}
			return
		}
		c.updates <- chatUpdate{status: "Sent"}
	}()
}

func (c *chatComponent) stream(ctx context.Context) {
	items, activity, err := c.client.history(ctx, 100)
	if err == nil {
		c.updates <- chatUpdate{items: items, status: activityStatus(activity)}
	}
	if err := c.client.subscribe(ctx, func(payload chatPayload) {
		c.updates <- chatUpdate{items: payload.Items, status: activityStatus(payload.Activity)}
	}); err != nil && ctx.Err() == nil {
		c.updates <- chatUpdate{err: err}
	}
}

func activityStatus(activity *chatActivity) string {
	if activity == nil || activity.Phase == "" {
		return "Ready"
	}
	return "Activity: " + activity.Phase
}

func (c chatClient) history(ctx context.Context, limit int) ([]chatItem, *chatActivity, error) {
	req, err := authedRequest(c.state, http.MethodGet, fmt.Sprintf("/webchat/history?limit=%d", limit), nil)
	if err != nil {
		return nil, nil, err
	}
	req = req.WithContext(ctx)
	var response struct {
		Data chatPayload `json:"data"`
	}
	if err := doJSON(req, http.StatusOK, &response); err != nil {
		return nil, nil, err
	}
	return response.Data.Items, response.Data.Activity, nil
}

func (c chatClient) send(ctx context.Context, text string) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("text", text); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := authedRequest(c.state, http.MethodPost, "/webchat/messages", &body)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return doJSON(req, http.StatusAccepted, nil)
}

func (c chatClient) newChat(ctx context.Context) error {
	req, err := authedRequest(c.state, http.MethodPost, "/webchat/chats/new", nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	return doJSON(req, http.StatusOK, nil)
}

func (c chatClient) respond(ctx context.Context, text string) error {
	awaitID, reply, ok := strings.Cut(text, " ")
	if !ok || strings.TrimSpace(awaitID) == "" || strings.TrimSpace(reply) == "" {
		return fmt.Errorf("usage: /respond <await_id> <reply>")
	}
	body, _ := json.Marshal(map[string]string{"await_id": awaitID, "reply": strings.TrimSpace(reply)})
	req, err := authedRequest(c.state, http.MethodPost, "/webchat/awaits/respond", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	return doJSON(req, http.StatusOK, nil)
}

func (c chatClient) subscribe(ctx context.Context, onMessage func(chatPayload)) error {
	req, err := authedRequest(c.state, http.MethodGet, "/webchat/events", nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("events failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				var payload chatPayload
				if err := json.Unmarshal([]byte(data.String()), &payload); err == nil {
					onMessage(payload)
				}
				data.Reset()
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return scanner.Err()
}

func doJSON(req *http.Request, wantStatus int, out any) error {
	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != wantStatus {
		return fmt.Errorf("request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil || len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
