package telegram

import (
	"context"
	"github.com/yurika0211/lucky-agent/agent"
	"github.com/yurika0211/luckyagent-runtime/gateway"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type telegramMethodRecorder struct {
	mu      sync.Mutex
	methods []string
}

func (r *telegramMethodRecorder) add(method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.methods = append(r.methods, method)
}

func (r *telegramMethodRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.methods))
	copy(out, r.methods)
	return out
}

func newCaptureBotAdapter(t *testing.T) (*Adapter, func(), *telegramMethodRecorder) {
	t.Helper()

	recorder := &telegramMethodRecorder{}
	bot, err := newMockBot(func(r *http.Request) map[string]any {
		switch {
		case containsMethod(r.URL.Path, "getMe"):
			return map[string]any{
				"ok": true,
				"result": map[string]any{
					"id":         123456789,
					"is_bot":     true,
					"first_name": "TestBot",
					"username":   "testbot",
				},
			}
		case containsMethod(r.URL.Path, "sendMessage"):
			recorder.add("sendMessage")
			return map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 42,
					"chat": map[string]any{
						"id": 12345,
					},
					"text": "ok",
				},
			}
		case containsMethod(r.URL.Path, "sendPhoto"):
			recorder.add("sendPhoto")
			return map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 43,
					"chat": map[string]any{
						"id": 12345,
					},
				},
			}
		case containsMethod(r.URL.Path, "sendDocument"):
			recorder.add("sendDocument")
			return map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 44,
					"chat": map[string]any{
						"id": 12345,
					},
				},
			}
		default:
			return defaultMockBotResponse(r)
		}
	})
	if err != nil {
		t.Fatalf("create mock bot: %v", err)
	}

	adapter := NewAdapterWithClient(Config{Token: bot.Token}, bot)
	cleanup := func() {}
	return adapter, cleanup, recorder
}

func TestSendAssistantResponsePhotoDirective(t *testing.T) {
	adapter, cleanup, recorder := newCaptureBotAdapter(t)
	defer cleanup()

	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "chart.png")
	if err := os.WriteFile(imagePath, []byte("fake image data"), 0600); err != nil {
		t.Fatalf("write temp image: %v", err)
	}

	handler := NewHandler(adapter, nil)
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
	}

	response := "Here is the chart\n\ntg://photo " + imagePath + " chart"
	if err := handler.sendAssistantResponse(context.Background(), msg, response); err != nil {
		t.Fatalf("sendAssistantResponse: %v", err)
	}

	methods := strings.Join(recorder.snapshot(), ",")
	if methods != "sendMessage,sendPhoto" {
		t.Fatalf("unexpected send sequence: %s", methods)
	}
}

func TestSendAssistantResponseBareDocumentPathStaysText(t *testing.T) {
	adapter, cleanup, recorder := newCaptureBotAdapter(t)
	defer cleanup()

	tmpDir := t.TempDir()
	docPath := filepath.Join(tmpDir, "report.pdf")
	if err := os.WriteFile(docPath, []byte("%PDF-1.4"), 0600); err != nil {
		t.Fatalf("write temp doc: %v", err)
	}

	handler := NewHandler(adapter, nil)
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
	}

	if err := handler.sendAssistantResponse(context.Background(), msg, docPath); err != nil {
		t.Fatalf("sendAssistantResponse: %v", err)
	}

	methods := recorder.snapshot()
	if len(methods) != 1 || methods[0] != "sendMessage" {
		t.Fatalf("unexpected methods: %#v", methods)
	}
}

func TestSendAssistantResponseMissingMediaFallsBackToText(t *testing.T) {
	adapter, cleanup, recorder := newCaptureBotAdapter(t)
	defer cleanup()

	handler := NewHandler(adapter, nil)
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
	}

	if err := handler.sendAssistantResponse(context.Background(), msg, "截图如下\nMEDIA:/tmp/screenshot-removed.png"); err != nil {
		t.Fatalf("sendAssistantResponse: %v", err)
	}
	methods := recorder.snapshot()
	if len(methods) != 1 || methods[0] != "sendMessage" {
		t.Fatalf("expected missing media to fall back to one text message, got %#v", methods)
	}
}

func TestSendComputerObservationSendsPhoto(t *testing.T) {
	adapter, cleanup, recorder := newCaptureBotAdapter(t)
	defer cleanup()

	path := filepath.Join(t.TempDir(), "frame.png")
	if err := os.WriteFile(path, []byte("fake image data"), 0600); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	handler := NewHandler(adapter, nil)
	msg := &gateway.Message{Chat: gateway.Chat{ID: "12345", Type: gateway.ChatPrivate}}
	handler.sendComputerObservation(context.Background(), msg, &agent.ObservationEvent{FrameID: "frame-1", FilePath: path})

	methods := recorder.snapshot()
	if len(methods) != 1 || methods[0] != "sendPhoto" {
		t.Fatalf("expected observation photo upload, got %#v", methods)
	}
}

func TestSendAssistantResponseDocumentMediaDoesNotSendRandomMeme(t *testing.T) {
	adapter, cleanup, recorder := newCaptureBotAdapter(t)
	defer cleanup()

	tmpDir := t.TempDir()
	docPath := filepath.Join(tmpDir, "report.docx")
	if err := os.WriteFile(docPath, []byte("fake docx"), 0600); err != nil {
		t.Fatalf("write temp doc: %v", err)
	}
	memePath := filepath.Join(tmpDir, "meme.png")
	if err := os.WriteFile(memePath, []byte("fake image data"), 0600); err != nil {
		t.Fatalf("write meme file: %v", err)
	}

	handler := NewHandler(adapter, nil)
	handler.memeDir = tmpDir
	handler.memeProbability = 1
	handler.memeCooldown = 0
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
	}

	if err := handler.sendAssistantResponse(context.Background(), msg, "Done\nMEDIA:"+docPath); err != nil {
		t.Fatalf("sendAssistantResponse: %v", err)
	}

	methods := strings.Join(recorder.snapshot(), ",")
	if methods != "sendMessage,sendDocument" {
		t.Fatalf("expected no random meme after document media, got sequence: %s", methods)
	}
}

func TestSendAssistantResponseRandomMeme(t *testing.T) {
	adapter, cleanup, recorder := newCaptureBotAdapter(t)
	defer cleanup()

	tmpDir := t.TempDir()
	memePath := filepath.Join(tmpDir, "meme.png")
	if err := os.WriteFile(memePath, []byte("fake image data"), 0600); err != nil {
		t.Fatalf("write meme file: %v", err)
	}

	handler := NewHandler(adapter, nil)
	handler.memeDir = tmpDir
	handler.memeProbability = 1
	handler.memeCooldown = 0
	handler.memeRand = rand.New(rand.NewSource(1))
	handler.memeNow = func() time.Time { return time.Unix(1000, 0) }

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
	}

	if err := handler.sendAssistantResponse(context.Background(), msg, "搞定了，已经处理完成"); err != nil {
		t.Fatalf("sendAssistantResponse: %v", err)
	}

	methods := strings.Join(recorder.snapshot(), ",")
	if methods != "sendMessage,sendPhoto" {
		t.Fatalf("unexpected send sequence: %s", methods)
	}
}

func TestSendAssistantResponseRandomMemeRespectsCooldown(t *testing.T) {
	adapter, cleanup, recorder := newCaptureBotAdapter(t)
	defer cleanup()

	tmpDir := t.TempDir()
	memePath := filepath.Join(tmpDir, "meme.png")
	if err := os.WriteFile(memePath, []byte("fake image data"), 0600); err != nil {
		t.Fatalf("write meme file: %v", err)
	}

	currentTime := time.Unix(1000, 0)
	handler := NewHandler(adapter, nil)
	handler.memeDir = tmpDir
	handler.memeProbability = 1
	handler.memeCooldown = 10 * time.Minute
	handler.memeRand = rand.New(rand.NewSource(1))
	handler.memeNow = func() time.Time { return currentTime }

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
	}

	if err := handler.sendAssistantResponse(context.Background(), msg, "搞定了，已经处理完成"); err != nil {
		t.Fatalf("first sendAssistantResponse: %v", err)
	}
	if err := handler.sendAssistantResponse(context.Background(), msg, "搞定了，再次完成"); err != nil {
		t.Fatalf("second sendAssistantResponse: %v", err)
	}

	methods := strings.Join(recorder.snapshot(), ",")
	if methods != "sendMessage,sendPhoto,sendMessage" {
		t.Fatalf("unexpected send sequence with cooldown: %s", methods)
	}
}

func TestShouldReportMediaDeliveryOnlyForLargeOrBatchedMedia(t *testing.T) {
	smallPath := filepath.Join(t.TempDir(), "small.pdf")
	if err := os.WriteFile(smallPath, []byte("small"), 0o600); err != nil {
		t.Fatalf("write small media: %v", err)
	}
	if shouldReportMediaDelivery([]outboundMedia{{Kind: outboundMediaDocument, Source: smallPath}}) {
		t.Fatal("small single document should not add progress messages")
	}

	largePath := filepath.Join(t.TempDir(), "large.pdf")
	if err := os.WriteFile(largePath, make([]byte, 5*1024*1024), 0o600); err != nil {
		t.Fatalf("write large media: %v", err)
	}
	if !shouldReportMediaDelivery([]outboundMedia{{Kind: outboundMediaDocument, Source: largePath}}) {
		t.Fatal("large document should report delivery progress")
	}
	if !shouldReportMediaDelivery([]outboundMedia{{Kind: outboundMediaPhoto, Source: "one"}, {Kind: outboundMediaPhoto, Source: "two"}}) {
		t.Fatal("multiple media items should report delivery progress")
	}
}
