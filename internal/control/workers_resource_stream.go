package control

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/krun-dev/krun/internal/adapters/configstream"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/runtimeconfig"
	"github.com/krun-dev/krun/internal/securetoken"
)

func (s *Server) runtimeResources() *runtimeconfig.ResourceHub {
	s.runtimeHubOnce.Do(func() {
		s.runtimeHub = runtimeconfig.NewResourceHub(s.Store, s.Config.Workers.RuntimeConfig)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-s.Store.RuntimeDone():
				cancel()
			case <-ctx.Done():
			}
		}()
		go func() {
			for ctx.Err() == nil {
				_ = s.Store.ListenRuntimeNotifications(ctx, s.runtimeHub.Invalidate)
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}()
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					check, cancel := context.WithTimeout(ctx, 2*time.Second)
					_ = s.runtimeHub.Verify(check)
					cancel()
				}
			}
		}()
	})
	return s.runtimeHub
}

func writeResourceFrame(ctx context.Context, conn *websocket.Conn, frame configstream.Frame, frameBytes, resourceBytes int) error {
	if len(frame.Error) > configstream.MaxFrameErrorBytes {
		return errors.New("configuration error exceeds protocol limit")
	}
	raw := configstream.Marshal(frame)
	if len(raw) > resourceBytes {
		return errors.New("configuration message exceeds resource budget")
	}
	write := func(data []byte) error {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return conn.Write(bounded, websocket.MessageBinary, data)
	}
	if len(raw) <= frameBytes {
		return write(raw)
	}
	chunkBytes := frameBytes - 256
	parts := (len(raw) + chunkBytes - 1) / chunkBytes
	for i := 0; i < parts; i++ {
		chunk := configstream.Frame{Type: "chunk", RequestID: frame.RequestID, Nonce: frame.Nonce, Part: uint32(i), Parts: uint32(parts), Chunk: raw[i*chunkBytes : min((i+1)*chunkBytes, len(raw))]}
		if err := write(configstream.Marshal(chunk)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) workerRuntimeResourceWatch(w http.ResponseWriter, r *http.Request) {
	pool, ok := s.Config.Workers.RuntimePool(r.Header.Get("X-Runtime-Pool"))
	if !ok || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+pool.RuntimeToken)) != 1 {
		s.writeError(w, r, forbidden())
		return
	}
	hub := s.runtimeResources()
	if !hub.TryConnect() {
		s.writeError(w, r, problem(503, "CONFIG_CAPACITY", "Configuration connection limit reached"))
		return
	}
	defer hub.Disconnect()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	limits := s.Config.Workers.RuntimeConfig
	_ = limits.Validate()
	conn.SetReadLimit(int64(limits.FrameBytes))
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	go func() {
		select {
		case <-s.Store.RuntimeDone():
			cancel()
		case <-ctx.Done():
		}
	}()
	welcomeCtx, stopWelcome := context.WithTimeout(ctx, 5*time.Second)
	messageType, raw, err := conn.Read(welcomeCtx)
	stopWelcome()
	if err != nil || messageType != websocket.MessageBinary {
		return
	}
	hello, err := configstream.Unmarshal(raw)
	if err != nil || hello.Type != "hello" || hello.PodUID == "" || len(hello.PodUID) > 128 || len(hello.Refs) > 256 {
		return
	}
	initial, stopInitial := context.WithTimeout(ctx, 2*time.Second)
	defer stopInitial()
	if s.workers == nil || s.workers.VerifyRuntimeSession(initial, pool, hello.PodUID, hello.SessionProof) != nil {
		return
	}
	confirmed := time.Now()
	epoch, err := s.Store.RuntimeEpoch(initial)
	if err != nil {
		return
	}
	var nonce uint64
	send := func(frame configstream.Frame) error {
		nonce++
		frame.Nonce = nonce
		if frame.Epoch == "" {
			frame.Epoch = epoch
		}
		return writeResourceFrame(ctx, conn, frame, limits.FrameBytes, limits.ResourceBytes)
	}
	if hello.Epoch != "" && hello.Epoch != epoch {
		_ = send(configstream.Frame{Type: "reset"})
		return
	}
	session := securetoken.Random()
	if err = s.Store.OpenRuntimeSession(initial, pool.ID, hello.PodUID, epoch, session); err != nil {
		return
	}
	rejected := false
	defer func() {
		closed, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Store.CloseRuntimeSession(closed, hello.PodUID, session, rejected)
	}()
	if err = send(configstream.Frame{Type: "welcome", Fresh: true, ConfirmedMillis: confirmed.UnixMilli()}); err != nil {
		return
	}
	messages := make(chan configstream.Frame, 8)
	go func() {
		defer cancel()
		for {
			kind, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if kind != websocket.MessageBinary {
				return
			}
			frame, err := configstream.Unmarshal(raw)
			if err != nil {
				return
			}
			select {
			case messages <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	wake := make(chan struct{}, 1)
	roots := map[string]domain.ResourceRef{}
	known := map[string]domain.RuntimeResource{}
	clientVersions := map[string]domain.ResourceRef{}
	acked := map[string]domain.ResourceRef{}
	inventoryRequested := false
	pruneAcks := false
	lastHeartbeat := time.Time{}
	stopWatch := func() {}
	defer func() { stopWatch() }()
	refs := []domain.ResourceRef{}
	register := func() {
		seen := map[string]bool{}
		refs = nil
		pending := []domain.ResourceRef{}
		for _, ref := range roots {
			pending = append(pending, ref)
		}
		for len(pending) > 0 {
			ref := pending[0]
			pending = pending[1:]
			if seen[ref.Key()] {
				continue
			}
			seen[ref.Key()] = true
			if resource, ok := known[ref.Key()]; ok {
				refs = append(refs, resource.ResourceRef)
				if !resource.Deleted {
					pending = append(pending, resource.Dependencies...)
				}
			} else {
				refs = append(refs, ref)
			}
		}
		next := hub.Watch(pool.ID, refs, wake)
		stopWatch()
		stopWatch = next
		for key := range known {
			if !seen[key] {
				if known[key].Kind == "deployment" {
					pruneAcks = true
				}
				delete(known, key)
				delete(acked, key)
				if inventoryRequested {
					delete(clientVersions, key)
				}
			}
		}
	}
	load := func(request configstream.Frame) error {
		if len(request.Refs) == 0 || len(request.Refs) > 256 {
			return errors.New("invalid resource batch")
		}
		if request.Type == "subscribe" || request.Type == "resolve" {
			for _, ref := range request.Refs {
				roots[ref.Key()] = ref
			}
			register()
		}
		if len(refs) > limits.MaxSubscriptions {
			return errors.New("subscription limit reached")
		}
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var current string
		var resources []domain.RuntimeResource
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			current, resources, err = hub.Resources(bounded, pool.ID, request.Refs)
			if err != nil {
				break
			}
			if request.Type != "resolve" && request.Type != "subscribe" {
				break
			}
			expanded := make([]domain.ResourceRef, 0, len(resources))
			for _, resource := range resources {
				expanded = append(expanded, resource.ResourceRef)
			}
			stopExpanded := hub.Watch(pool.ID, expanded, wake)
			valid, checkErr := hub.Recheck(bounded, current, resources)
			if valid && checkErr == nil {
				defer stopExpanded()
				break
			}
			stopExpanded()
			err = checkErr
			if err != nil {
				break
			}
			err = errors.New("configuration changed during subscription")
		}
		if err != nil {
			return send(configstream.Frame{Type: "error", RequestID: request.RequestID, Error: "CONFIG_UNAVAILABLE"})
		}
		if current != epoch {
			_ = send(configstream.Frame{Type: "reset", Epoch: current})
			return errors.New("configuration epoch changed")
		}
		changed := []domain.RuntimeResource{}
		for _, resource := range resources {
			previous, had := known[resource.Key()]
			client, resumed := clientVersions[resource.Key()]
			matchesClient := resumed && client.Incarnation == resource.Incarnation && client.Generation == resource.Generation && !resource.Deleted
			if request.RequestID != 0 || (!had && !matchesClient) || had && (previous.Incarnation != resource.Incarnation || previous.Generation != resource.Generation || previous.Deleted != resource.Deleted) {
				changed = append(changed, resource)
			}
			known[resource.Key()] = resource
		}
		register()
		if len(refs) > limits.MaxSubscriptions {
			return errors.New("expanded subscription limit reached")
		}
		if pruneAcks {
			if err = s.Store.PruneRuntimeResourceAcks(bounded, hello.PodUID, session, refs); err != nil {
				return err
			}
			pruneAcks = false
		}
		if len(resources) > 0 || request.RequestID != 0 {
			versions := make([]domain.ResourceRef, 0, len(resources))
			for _, resource := range resources {
				versions = append(versions, resource.ResourceRef)
			}
			return send(configstream.Frame{Type: "snapshot", RequestID: request.RequestID, Resources: changed, Refs: versions})
		}
		return nil
	}
	if len(hello.Refs) > 0 {
		hello.Type = "subscribe"
		if err = load(hello); err != nil {
			return
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	refresh := func() error {
		stale := hub.Stale(pool.ID, refs)
		for start := 0; start < len(stale); start += 128 {
			if err := load(configstream.Frame{Type: "refresh", Refs: stale[start:min(start+128, len(stale))]}); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-messages:
			if frame.Epoch != "" && frame.Epoch != epoch {
				_ = send(configstream.Frame{Type: "reset"})
				return
			}
			switch frame.Type {
			case "resume":
				if len(clientVersions)+len(frame.Refs) > limits.MaxSubscriptions {
					return
				}
				for _, ref := range frame.Refs {
					clientVersions[ref.Key()] = ref
				}
			case "ready":
				inventoryRequested = true
			case "resolve", "subscribe":
				if err = load(frame); err != nil {
					return
				}
			case "unsubscribe":
				for _, ref := range frame.Refs {
					delete(roots, ref.Key())
				}
				register()
				if err = s.Store.PruneRuntimeResourceAcks(ctx, hello.PodUID, session, refs); err != nil {
					return
				}
				pruneAcks = false
			case "ack":
				if len(frame.Refs) > 256 {
					return
				}
				for _, ref := range frame.Refs {
					sent, ok := known[ref.Key()]
					if !ok || sent.Generation != ref.Generation || sent.Incarnation != ref.Incarnation {
						return
					}
					acked[ref.Key()] = ref
				}
				if err = s.Store.RecordRuntimeResourceAcks(ctx, pool.ID, hello.PodUID, epoch, session, frame.Refs); err != nil {
					return
				}
			case "nack":
				rejected = true
				return
			case "ping":
			default:
				return
			}
		case <-wake:
			if err = refresh(); err != nil {
				return
			}
		case <-ticker.C:
			if err = refresh(); err != nil {
				return
			}
			checked, fresh := hub.Confirmed(pool.ID, refs)
			if len(refs) == 0 {
				_, checked, fresh = hub.Authority()
			}
			current, _, _ := hub.Authority()
			if current != "" && current != epoch {
				_ = send(configstream.Frame{Type: "reset", Epoch: current})
				return
			}
			complete := inventoryRequested
			for _, ref := range refs {
				applied, ok := acked[ref.Key()]
				if !ok || applied.Incarnation != ref.Incarnation || applied.Generation != ref.Generation {
					complete = false
					break
				}
			}
			if time.Since(lastHeartbeat) >= 5*time.Second {
				lastHeartbeat = time.Now()
				if err = s.Store.TouchRuntimeSession(ctx, hello.PodUID, session, complete, fresh); err != nil {
					return
				}
			}
			if err = send(configstream.Frame{Type: "status", Fresh: fresh, Applied: complete, ConfirmedMillis: checked.UnixMilli()}); err != nil {
				return
			}
		}
	}
}
