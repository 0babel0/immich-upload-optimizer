package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"net/http"
	"strings"
	"sync"
)

// WebSocket42 A message starting with the number 42 and then a JSON array. The 1st element is the action/event e.g. on_upload_success, on_asset_delete. Other elements vary depending on the action
type WebSocket42 []any

func (wsMsg WebSocket42) getUploadSuccessAsset() Asset {
	if len(wsMsg) < 2 {
		return nil
	}
	if v, ok := wsMsg[1].(map[string]any); ok {
		return v
	}
	return nil
}

func (wsMsg WebSocket42) getUploadReadyAsset() Asset {
	if len(wsMsg) < 2 {
		return nil
	}
	if v, ok := wsMsg[1].(map[string]any); ok {
		if a, ok := v["asset"].(map[string]any); ok {
			return a
		}
	}
	return nil
}

func handleWebSocketConn(cliConn, srvConn *websocket.Conn, logger *customLogger) {
	var wg sync.WaitGroup
	wg.Add(2)
	logger.SetErrPrefix("websocket proxy")
	go func() {
		defer wg.Done()
		var err error
		var msgType int
		var message []byte
		for {
			if msgType, message, err = srvConn.ReadMessage(); logger.Error(err, "srv ReadMessage") {
				break
			}
			//fmt.Printf("SRV: Type: %d Message: %s\n", msgType, message)
			if msgType == websocket.TextMessage && len(message) > 2 && bytes.Equal(message[:2], []byte("42")) {
				var wsMsg WebSocket42
				if err = json.Unmarshal(message[2:], &wsMsg); logger.Error(err, "json unmarshal") {
					continue
				}
				// Event names are versioned by Immich (e.g. AssetUploadReadyV1 -> ...V2), so
				// don't match on the action: try both payload shapes that can carry an asset.
				// toOriginalAsset is a no-op on payloads without a known checksum.
				assets := make([]Asset, 0, 2)
				if a := wsMsg.getUploadSuccessAsset(); a != nil {
					assets = append(assets, a)
				}
				if a := wsMsg.getUploadReadyAsset(); a != nil {
					assets = append(assets, a)
				}
				if len(assets) > 0 {
					mapLock.RLock()
					for _, asset := range assets {
						asset.toOriginalAsset()
					}
					mapLock.RUnlock()
					if message, err = json.Marshal(wsMsg); logger.Error(err, "json encode") {
						continue
					}
					message = append([]byte("42"), message...)
				}
			}
			if err = cliConn.WriteMessage(msgType, message); err != nil {
				if !errors.Is(err, websocket.ErrCloseSent) {
					logger.Error(err, "cli WriteMessage")
					break
				}
				break
			}
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		var msgType int
		var message []byte
		for {
			if msgType, message, err = cliConn.ReadMessage(); err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived, websocket.CloseAbnormalClosure) {
					logger.Error(err, "client disconnect")
					break
				}
				logger.Error(err, "cli ReadMessage")
				break
			}
			if err = srvConn.WriteMessage(msgType, message); logger.Error(err, "srv WriteMessage") {
				break
			}
		}
	}()
	wg.Wait()
}

func upgradeWebSocketRequest(w http.ResponseWriter, r *http.Request, logger *customLogger) {
	var err error
	logger.SetErrPrefix("websocket")
	logger.Printf("websocket proxy: client connection upgrade")
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	var cliConn, srvConn *websocket.Conn
	if cliConn, err = upgrader.Upgrade(w, r, nil); logger.Error(err, "upgrade") {
		return
	}
	defer cliConn.Close()
	if srvConn, _, err = websocket.DefaultDialer.Dial("ws"+upstreamURL[strings.Index(upstreamURL, ":"):]+r.URL.String(), webSocketSafeHeader(r.Header)); logger.Error(err, "dial") {
		return
	}
	defer srvConn.Close()
	handleWebSocketConn(cliConn, srvConn, logger)
}
