package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type cliState struct {
	BaseURL     string `json:"base_url"`
	Email       string `json:"email"`
	CookieName  string `json:"cookie_name"`
	CookieValue string `json:"cookie_value"`
	CSRFToken   string `json:"csrf_token"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("missing command")
	}
	switch args[0] {
	case "dev-login":
		return devLogin(args[1:])
	case "send":
		return sendMessage(args[1:])
	case "history":
		return history(args[1:])
	case "respond":
		return respond(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  nexuscli dev-login --base-url http://localhost:8080 --email dev@example.com
  nexuscli send "hello"
  nexuscli history --limit 20
  nexuscli respond --await-id await_123 --reply "approve"`)
}

func devLogin(args []string) error {
	fs := flag.NewFlagSet("dev-login", flag.ContinueOnError)
	baseURL := fs.String("base-url", "http://localhost:8080", "gateway base URL")
	email := fs.String("email", "dev@example.com", "webchat email identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"email": *email})
	req, err := http.NewRequest(http.MethodPost, joinURL(*baseURL, "/webchat/dev/session"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dev login failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	cookie := firstCookie(resp)
	if cookie == nil || cookie.Value == "" {
		return errors.New("dev login response did not include a session cookie")
	}
	var payload struct {
		Data struct {
			Email     string `json:"email"`
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if payload.Data.CSRFToken == "" {
		return errors.New("dev login response did not include csrf_token")
	}
	state := cliState{
		BaseURL:     strings.TrimRight(*baseURL, "/"),
		Email:       payload.Data.Email,
		CookieName:  cookie.Name,
		CookieValue: cookie.Value,
		CSRFToken:   payload.Data.CSRFToken,
	}
	if err := saveState(state); err != nil {
		return err
	}
	fmt.Printf("logged in as %s\n", state.Email)
	return nil
}

func sendMessage(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return errors.New("missing message text")
	}
	state, err := loadState()
	if err != nil {
		return err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("text", text); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := authedRequest(state, http.MethodPost, "/webchat/messages", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return doAndPrint(req, http.StatusAccepted)
}

func history(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "history item limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := loadState()
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/webchat/history?limit=%d", *limit)
	req, err := authedRequest(state, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return doAndPrint(req, http.StatusOK)
}

func respond(args []string) error {
	fs := flag.NewFlagSet("respond", flag.ContinueOnError)
	awaitID := fs.String("await-id", "", "await id")
	reply := fs.String("reply", "", "reply text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *awaitID == "" {
		return errors.New("missing --await-id")
	}
	if strings.TrimSpace(*reply) == "" {
		return errors.New("missing --reply")
	}
	state, err := loadState()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"await_id": *awaitID, "reply": *reply})
	req, err := authedRequest(state, http.MethodPost, "/webchat/awaits/respond", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doAndPrint(req, http.StatusOK)
}

func authedRequest(state cliState, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, joinURL(state.BaseURL, path), body)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: state.CookieName, Value: state.CookieValue})
	if state.CSRFToken != "" {
		req.Header.Set("X-CSRF-Token", state.CSRFToken)
	}
	return req, nil
}

func doAndPrint(req *http.Request, wantStatus int) error {
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
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		fmt.Println(string(raw))
		return nil
	}
	fmt.Println(out.String())
	return nil
}

func firstCookie(resp *http.Response) *http.Cookie {
	for _, cookie := range resp.Cookies() {
		if cookie.Value != "" {
			return cookie
		}
	}
	return nil
}

func loadState() (cliState, error) {
	path, err := statePath()
	if err != nil {
		return cliState{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cliState{}, fmt.Errorf("read state: %w; run dev-login first", err)
	}
	var state cliState
	if err := json.Unmarshal(raw, &state); err != nil {
		return cliState{}, err
	}
	if state.BaseURL == "" || state.CookieName == "" || state.CookieValue == "" {
		return cliState{}, errors.New("state is incomplete; run dev-login again")
	}
	return state, nil
}

func saveState(state cliState) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func statePath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("NEXUSCLI_STATE")); override != "" {
		return override, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nexus", "cli.json"), nil
}

func joinURL(baseURL, path string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if parsed, err := url.Parse(path); err == nil && parsed.IsAbs() {
		return path
	}
	return baseURL + "/" + strings.TrimLeft(path, "/")
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}
