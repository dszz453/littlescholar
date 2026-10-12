package task

import (
	"context"
	"log"
	"sync"
	"time"
)

// Job 任务
type Job struct {
	ID      string
	Name    string
	Payload interface{}
	Handler func(ctx context.Context, payload interface{}) error
	Retry   int
	MaxRetry int
}

// WorkerPool 任务工作池
type WorkerPool struct {
	workers  int
	queue    chan *Job
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	started  bool
	mu       sync.Mutex
}

// NewWorkerPool 创建工作池
func NewWorkerPool(workers, queueSize int) *WorkerPool {
	if workers <= 0 {
		workers = 3
	}
	if queueSize <= 0 {
		queueSize = 1000
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkerPool{
		workers: workers,
		queue:   make(chan *Job, queueSize),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start 启动工作池
func (p *WorkerPool) Start() {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()

	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	log.Printf("[task] worker pool started with %d workers", p.workers)
}

// worker 单个工作者
func (p *WorkerPool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case job, ok := <-p.queue:
			if !ok {
				return
			}
			p.runJob(id, job)
		}
	}
}

// runJob 执行任务（带重试）
func (p *WorkerPool) runJob(workerID int, job *Job) {
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Minute)
	defer cancel()

	err := job.Handler(ctx, job.Payload)
	if err == nil {
		return
	}

	maxRetry := job.MaxRetry
	if maxRetry <= 0 {
		maxRetry = 2
	}
	if job.Retry < maxRetry {
		job.Retry++
		delay := time.Duration(job.Retry) * 3 * time.Second
		log.Printf("[task] job %s failed (attempt %d/%d): %v, retry in %v",
			job.ID, job.Retry, maxRetry, err, delay)
		time.AfterFunc(delay, func() {
			p.Submit(job)
		})
		return
	}
	log.Printf("[task] job %s permanently failed after %d attempts: %v", job.ID, job.Retry+1, err)
}

// Submit 提交任务
func (p *WorkerPool) Submit(job *Job) bool {
	select {
	case p.queue <- job:
		return true
	default:
		log.Printf("[task] queue full, job %s rejected", job.ID)
		return false
	}
}

// Stop 停止工作池
func (p *WorkerPool) Stop() {
	p.cancel()
	close(p.queue)
	p.wg.Wait()
	log.Printf("[task] worker pool stopped")
}

// QueueLen 当前队列长度
func (p *WorkerPool) QueueLen() int {
	return len(p.queue)
}
