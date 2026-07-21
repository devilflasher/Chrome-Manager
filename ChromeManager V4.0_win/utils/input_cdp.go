package utils

import (
	"bufio"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type cdpPageTarget struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func (im *InputManager) inputTextToFocusedPageElement(debugPort int, text string, overwrite bool, delayed bool) error {
	targets, err := getCDPPageTargets(debugPort)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no page targets")
	}

	var lastErr error
	for _, target := range targets {
		if target.WebSocketDebuggerURL == "" || isInputCDPIgnoredURL(target.URL) {
			continue
		}
		ok, err := sendTextToCDPActiveElement(target.WebSocketDebuggerURL, text, overwrite, delayed)
		if err != nil {
			lastErr = err
			continue
		}
		if ok {
			return nil
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no focused editable page element")
}

func getCDPPageTargets(debugPort int) ([]cdpPageTarget, error) {
	client := http.Client{Timeout: 1200 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json", debugPort))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CDP target list returned %d", resp.StatusCode)
	}

	var targets []cdpPageTarget
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, err
	}

	pageTargets := make([]cdpPageTarget, 0, len(targets))
	for _, target := range targets {
		if target.Type == "page" {
			pageTargets = append(pageTargets, target)
		}
	}
	return pageTargets, nil
}

func isInputCDPIgnoredURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "devtools://")
}

func sendTextToCDPActiveElement(wsURL, text string, overwrite bool, delayed bool) (bool, error) {
	conn, reader, err := dialCDPWebSocket(wsURL)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	expression := fmt.Sprintf("(%s)(%q, %t, %t)", activeElementInputScript, text, overwrite, delayed)
	timeout := 2 * time.Second
	if delayed {
		timeout = time.Duration(len([]rune(text))*140+2500) * time.Millisecond
	}
	resp, err := sendCDPCommand(conn, reader, 1, "Runtime.evaluate", map[string]interface{}{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}, timeout)
	if err != nil {
		return false, err
	}

	result, _ := resp["result"].(map[string]interface{})
	evalResult, _ := result["result"].(map[string]interface{})
	value, _ := evalResult["value"].(bool)
	return value, nil
}

const activeElementInputScript = `async function(text, overwrite, delayed) {
  function deepActiveElement(root) {
    let active = root.activeElement;
    while (active && active.shadowRoot && active.shadowRoot.activeElement) {
      active = active.shadowRoot.activeElement;
    }
    return active;
  }

  if (document.visibilityState !== 'visible') return false;

  const el = deepActiveElement(document);
  if (!el) return false;
  const tag = (el.tagName || '').toUpperCase();
  const editable = tag === 'INPUT' || tag === 'TEXTAREA' || el.isContentEditable;
  if (!editable || el.disabled || el.readOnly) return false;

  el.focus();
  const sleep = (ms) => new Promise(resolve => setTimeout(resolve, ms));

  function dispatchInput(data, replacement) {
    let event;
    try {
      event = new InputEvent('input', {
        bubbles: true,
        cancelable: true,
        inputType: replacement ? 'insertReplacementText' : 'insertText',
        data
      });
    } catch (_) {
      event = new Event('input', {bubbles: true, cancelable: true});
    }
    el.dispatchEvent(event);
  }

  function setEditableText(value) {
    if (el.isContentEditable) {
      el.textContent = value;
      return;
    }
    el.value = value;
    const pos = el.value.length;
    if (typeof el.setSelectionRange === 'function') {
      try { el.setSelectionRange(pos, pos); } catch (_) {}
    }
  }

  function insertTextChunk(chunk) {
    if (el.isContentEditable) {
      const selection = window.getSelection && window.getSelection();
      if (selection && selection.rangeCount > 0 && el.contains(selection.anchorNode)) {
        const range = selection.getRangeAt(0);
        range.deleteContents();
        const node = document.createTextNode(chunk);
        range.insertNode(node);
        range.setStartAfter(node);
        range.setEndAfter(node);
        selection.removeAllRanges();
        selection.addRange(range);
      } else {
        el.appendChild(document.createTextNode(chunk));
      }
      return;
    }

    const start = el.selectionStart ?? el.value.length;
    const end = el.selectionEnd ?? el.value.length;
    if (typeof el.setRangeText === 'function') {
      el.setRangeText(chunk, start, end, 'end');
    } else {
      el.value = el.value.slice(0, start) + chunk + el.value.slice(end);
      const pos = start + chunk.length;
      if (typeof el.setSelectionRange === 'function') {
        try { el.setSelectionRange(pos, pos); } catch (_) {}
      }
    }
  }

  if (overwrite) {
    setEditableText('');
    dispatchInput('', true);
  }

  if (!delayed) {
    if (overwrite) {
      insertTextChunk(text);
    } else {
      insertTextChunk(text);
    }
    dispatchInput(text, overwrite);
    el.dispatchEvent(new Event('change', {bubbles: true}));
    return true;
  }

  const chars = Array.from(text);
  for (const ch of chars) {
    insertTextChunk(ch);
    dispatchInput(ch, false);
    await sleep(45 + Math.floor(Math.random() * 75));
  }

  el.dispatchEvent(new Event('change', {bubbles: true}));
  return true;
}`

func dialCDPWebSocket(rawURL string) (net.Conn, *bufio.Reader, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	host := parsed.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 1200*time.Millisecond)
	if err != nil {
		return nil, nil, err
	}

	keyBytes := make([]byte, 16)
	if _, err := crand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := parsed.RequestURI()
	if path == "" {
		path = "/"
	}

	request := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path,
		parsed.Host,
		key,
	)
	if _, err := conn.Write([]byte(request)); err != nil {
		conn.Close()
		return nil, nil, err
	}

	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if !strings.Contains(status, " 101 ") {
		conn.Close()
		return nil, nil, fmt.Errorf("unexpected websocket status: %s", strings.TrimSpace(status))
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, reader, nil
}

func sendCDPCommand(conn net.Conn, reader *bufio.Reader, id int, method string, params map[string]interface{}, timeout time.Duration) (map[string]interface{}, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"id":     id,
		"method": method,
		"params": params,
	})
	if err != nil {
		return nil, err
	}
	if err := writeClientWebSocketFrame(conn, payload); err != nil {
		return nil, err
	}

	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		frame, err := readServerWebSocketFrame(reader)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return nil, err
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(frame, &msg); err != nil {
			continue
		}
		if msgID, _ := msg["id"].(float64); int(msgID) != id {
			continue
		}
		if errPayload, exists := msg["error"]; exists {
			return nil, fmt.Errorf("CDP command %s failed: %v", method, errPayload)
		}
		return msg, nil
	}
	return nil, fmt.Errorf("CDP command %s timed out", method)
}

func writeClientWebSocketFrame(conn net.Conn, payload []byte) error {
	header := make([]byte, 0, 14)
	header = append(header, 0x81)
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 0xFFFF:
		header = append(header, 0x80|126)
		tmp := make([]byte, 2)
		binary.BigEndian.PutUint16(tmp, uint16(length))
		header = append(header, tmp...)
	default:
		header = append(header, 0x80|127)
		tmp := make([]byte, 8)
		binary.BigEndian.PutUint64(tmp, uint64(length))
		header = append(header, tmp...)
	}

	var maskKey [4]byte
	if _, err := crand.Read(maskKey[:]); err != nil {
		return err
	}
	header = append(header, maskKey[:]...)

	maskedPayload := make([]byte, len(payload))
	for i := range payload {
		maskedPayload[i] = payload[i] ^ maskKey[i%4]
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(maskedPayload)
	return err
}

func readServerWebSocketFrame(reader *bufio.Reader) ([]byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	opcode := header[0] & 0x0F
	length := uint64(header[1] & 0x7F)
	switch length {
	case 126:
		extended := make([]byte, 2)
		if _, err := io.ReadFull(reader, extended); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended))
	case 127:
		extended := make([]byte, 8)
		if _, err := io.ReadFull(reader, extended); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(extended)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}

	switch opcode {
	case 0x1:
		return payload, nil
	case 0x8:
		return nil, io.ErrClosedPipe
	case 0x9:
		return readServerWebSocketFrame(reader)
	default:
		return readServerWebSocketFrame(reader)
	}
}
