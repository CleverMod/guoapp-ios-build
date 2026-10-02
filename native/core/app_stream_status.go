package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

func (stream *nativeStreamServer) nativeEnsureServing(ctx context.Context) (bool, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	address := strings.TrimPrefix(stream.address, "http://")
	if stream.serving && stream.server != nil {
		dialer := net.Dialer{Timeout: 500 * time.Millisecond}
		connection, err := dialer.DialContext(ctx, "tcp4", address)
		if err == nil {
			connection.Close()
			return false, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		stream.serveError = publicError(err).Error()
		_ = stream.server.Close()
		stream.serving = false
	}
	if address == "" {
		address = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return false, fmt.Errorf("无法启动本机播放代理: %w", publicError(err))
	}
	restarted := stream.server != nil
	if restarted {
		stream.restarts++
	}
	if stream.address == "" {
		stream.address = "http://" + listener.Addr().String()
	}
	server := &http.Server{Handler: http.HandlerFunc(stream.nativeServe), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	stream.server, stream.serving = server, true
	go func() {
		err := server.Serve(listener)
		stream.mu.Lock()
		if stream.server == server {
			stream.serving = false
			if err != nil {
				stream.serveError = publicError(err).Error()
			}
		}
		stream.mu.Unlock()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			stream.downloader.recordDiagnostic(diagnosticEvent{Event: "playback.proxy_stopped", Message: publicError(err).Error()})
		}
	}()
	return restarted, nil
}

func (stream *nativeStreamServer) nativeRecord(session *nativeStreamSession, message string) {
	message = truncate(redactConfiguredString(&stream.downloader.cfg, message), 512)
	session.mu.Lock()
	session.events = append(session.events, time.Now().Format("15:04:05.000")+" "+message)
	if len(session.events) > 12 {
		session.events = session.events[len(session.events)-12:]
	}
	session.mu.Unlock()
}

func (engine *nativeEngine) nativePlaybackStatus(ctx context.Context, token string, ensure bool) (map[string]any, error) {
	engine.mu.Lock()
	choice, exists := engine.playbacks[token]
	stream := engine.stream
	engine.mu.Unlock()
	if !exists || stream == nil {
		return nil, errors.New("播放会话已结束，请重新播放")
	}
	restarted := false
	if ensure {
		var err error
		restarted, err = stream.nativeEnsureServing(ctx)
		if err != nil {
			return nil, err
		}
	}
	stream.mu.Lock()
	session := stream.sessions[choice.streamSession]
	result := map[string]any{"address": stream.address, "serving": stream.serving, "restarted": restarted, "restarts": stream.restarts, "lastError": stream.serveError}
	stream.mu.Unlock()
	if session == nil {
		return nil, errors.New("播放媒体会话已结束，请重新播放")
	}
	session.mu.Lock()
	result["events"] = append([]string{}, session.events...)
	session.events = nil
	session.mu.Unlock()
	return result, nil
}
