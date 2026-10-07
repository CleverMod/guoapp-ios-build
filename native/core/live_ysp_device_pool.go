package core

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const yspDeviceLinkLimit = 6

type yspDeviceSource interface {
	start()
	stop()
	reopen(string)
	resolve(context.Context, yspChannel) (yspDeviceEntry, error)
	accept(yspChannel, yspDeviceEntry)
	reject(yspChannel, yspDeviceEntry)
}

type yspDevicePool struct {
	mu           sync.Mutex
	control      chan struct{}
	slots        [3]*yspDeviceResolver
	standby      *yspDeviceResolver
	assignments  map[string]int
	lastRotation [3]time.Time
	cancel       context.CancelFunc
	life         context.Context
	refilling    bool
	lastRefill   time.Time
}

func newYSPDevicePool(client *http.Client, directory string) *yspDevicePool {
	pool := &yspDevicePool{control: make(chan struct{}, 1), assignments: map[string]int{}}
	for index := range pool.slots {
		device := newYSPDeviceResolver(client, directory)
		device.fast, device.warm = true, index == 0
		if index > 0 {
			device.path = filepath.Join(directory, "ysp-device-state-slot"+strconv.Itoa(index)+".json")
		}
		pool.slots[index] = device
	}
	pool.standby = newYSPDeviceResolver(client, directory)
	pool.standby.path, pool.standby.fast, pool.standby.warm = "", true, false
	return pool
}

func (pool *yspDevicePool) start() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool.cancel = cancel
	pool.life = ctx
	for _, device := range append(pool.slots[:], pool.standby) {
		device.mu.Lock()
		device.life = ctx
		device.mu.Unlock()
	}
	go pool.run(ctx)
}

func (pool *yspDevicePool) stop() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.cancel != nil {
		pool.cancel()
		pool.cancel = nil
	}
}

func (pool *yspDevicePool) run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		for index, device := range pool.slots {
			work, cancel := context.WithTimeout(ctx, 45*time.Second)
			device.mu.Lock()
			session := device.session
			renew := index == 0 && session != nil && time.Since(session.created) >= yspDeviceSessionTTL-5*time.Minute
			device.mu.Unlock()
			if renew {
				pool.rotate(work, index)
			}
			device.pulse(work)
			cancel()
			if index == 0 {
				pool.replenish()
			}
			if ctx.Err() != nil {
				return
			}
		}
		pool.standby.mu.Lock()
		ready := pool.standby.session != nil
		pool.standby.mu.Unlock()
		if ready {
			work, cancel := context.WithTimeout(ctx, 45*time.Second)
			pool.standby.pulse(work)
			cancel()
		} else {
			pool.replenish()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (pool *yspDevicePool) replenish() {
	pool.mu.Lock()
	if pool.refilling || pool.life == nil || pool.life.Err() != nil {
		pool.mu.Unlock()
		return
	}
	pool.refilling = true
	life, delay := pool.life, time.Until(pool.lastRefill.Add(15*time.Second))
	pool.mu.Unlock()
	go func() {
		defer func() {
			pool.mu.Lock()
			pool.refilling = false
			pool.mu.Unlock()
		}()
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-life.Done():
				return
			case <-timer.C:
			}
		}
		pool.mu.Lock()
		pool.lastRefill = time.Now()
		pool.mu.Unlock()
		ctx, cancel := context.WithTimeout(life, 45*time.Second)
		defer cancel()
		pool.standby.pulse(ctx)
	}()
}

func yspDeviceLinkedCount(device *yspDeviceResolver) int {
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.session == nil {
		return 0
	}
	return len(device.session.linked)
}

func (pool *yspDevicePool) slotFor(channel string) int {
	if yspWarmChannel(channel) {
		return 0
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if slot, exists := pool.assignments[channel]; exists {
		return slot
	}
	first, second := yspDeviceLinkedCount(pool.slots[1]), yspDeviceLinkedCount(pool.slots[2])
	slot := 1
	if second < first {
		slot = 2
	}
	pool.assignments[channel] = slot
	return slot
}

func (pool *yspDevicePool) takeStandby() *yspDeviceSession {
	standby := pool.standby
	select {
	case standby.control <- struct{}{}:
		defer func() { <-standby.control }()
	default:
		return nil
	}
	standby.mu.Lock()
	defer standby.mu.Unlock()
	session := standby.session
	if session == nil || time.Since(session.created) >= yspDeviceSessionTTL-5*time.Minute {
		return nil
	}
	standby.session, standby.loaded = nil, false
	standby.sessionRetry = time.Time{}
	return session
}

func (pool *yspDevicePool) rotate(ctx context.Context, slot int) error {
	device := pool.slots[slot]
	if err := device.lockControl(ctx); err != nil {
		return err
	}
	defer func() { <-device.control }()
	pool.mu.Lock()
	tooSoon := time.Since(pool.lastRotation[slot]) < 8*time.Second
	if !tooSoon {
		pool.lastRotation[slot] = time.Now()
	}
	pool.mu.Unlock()
	if tooSoon {
		return errors.New("央视频设备正在轮换，请稍后重试")
	}
	session := pool.takeStandby()
	if session == nil {
		state, err := yspNewDeviceState()
		if err != nil {
			return err
		}
		device.device, device.loaded = state, true
		session, err = device.bootstrapFast(ctx)
		if err != nil {
			return err
		}
	}
	if err := yspSaveDeviceState(device.path, session.device); err != nil {
		return errors.New("无法保存央视频轮换设备")
	}
	device.mu.Lock()
	device.session, device.device, device.loaded = session, session.device, true
	device.sessionRetry, device.bootFailures = time.Time{}, 0
	device.failures = map[string]yspDeviceFailure{}
	device.mu.Unlock()
	pool.mu.Lock()
	pool.lastRotation[slot] = time.Now()
	for channel, assigned := range pool.assignments {
		if assigned == slot {
			delete(pool.assignments, channel)
		}
	}
	pool.mu.Unlock()
	pool.replenish()
	return nil
}

func (pool *yspDevicePool) resolve(ctx context.Context, channel yspChannel) (yspDeviceEntry, error) {
	select {
	case pool.control <- struct{}{}:
		defer func() { <-pool.control }()
	case <-ctx.Done():
		return yspDeviceEntry{}, ctx.Err()
	}
	slot := pool.slotFor(channel.ID)
	device := pool.slots[slot]
	device.mu.Lock()
	session := device.session
	waiting := time.Now().Before(device.failures[channel.ID].retryAt)
	full := slot > 0 && session != nil && !session.linked[channel.ID] && len(session.linked) >= yspDeviceLinkLimit
	device.mu.Unlock()
	if waiting {
		return yspDeviceEntry{}, errors.New("央视频设备频道正在等待重试")
	}
	if full {
		other := 3 - slot
		if yspDeviceLinkedCount(pool.slots[other]) < yspDeviceLinkLimit {
			slot, device = other, pool.slots[other]
		} else if err := pool.rotate(ctx, slot); err != nil {
			if !errors.Is(err, context.Canceled) {
				device.mu.Lock()
				device.recordFailureLocked(channel.ID)
				device.mu.Unlock()
			}
			return yspDeviceEntry{}, err
		}
		pool.mu.Lock()
		pool.assignments[channel.ID] = slot
		pool.mu.Unlock()
	}
	entry, err := device.resolve(ctx, channel)
	if err != nil && yspDeviceInvalidates(err) && ctx.Err() == nil {
		if rotateErr := pool.rotate(ctx, slot); rotateErr == nil {
			device.reopen(channel.ID)
			entry, err = device.resolve(ctx, channel)
		}
	}
	return entry, err
}

func (pool *yspDevicePool) retryPlaylist(ctx context.Context, channel yspChannel, rejected yspDeviceEntry, rotate bool) (yspDeviceEntry, error) {
	device := rejected.owner
	if device == nil {
		return yspDeviceEntry{}, errors.New("央视频设备线路身份无效")
	}
	device.mu.Lock()
	if entry := device.entries[channel.ID]; entry.address == rejected.address {
		delete(device.entries, channel.ID)
	}
	delete(device.failures, channel.ID)
	device.mu.Unlock()
	if rotate {
		for slot, candidate := range pool.slots {
			if candidate == device {
				if err := pool.rotate(ctx, slot); err != nil {
					return yspDeviceEntry{}, err
				}
				break
			}
		}
	}
	return pool.resolve(ctx, channel)
}

func (pool *yspDevicePool) reopen(channel string) {
	for _, device := range pool.slots {
		device.reopen(channel)
	}
}

func (pool *yspDevicePool) accept(channel yspChannel, entry yspDeviceEntry) {
	if entry.owner != nil {
		entry.owner.accept(channel, entry)
	}
}

func (pool *yspDevicePool) reject(channel yspChannel, entry yspDeviceEntry) {
	if entry.owner != nil {
		entry.owner.mu.Lock()
		_, exists := entry.owner.entries[channel.ID]
		if !exists && !time.Now().Before(entry.owner.failures[channel.ID].retryAt) {
			entry.owner.recordFailureLocked(channel.ID)
		}
		entry.owner.mu.Unlock()
		entry.owner.reject(channel, entry)
	}
}
