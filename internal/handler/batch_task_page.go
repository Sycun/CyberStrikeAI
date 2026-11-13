package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

func copyBatchTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// SnapshotBatchQueue owns every mutable field before HTTP serialization. A
// refresh must not race the worker updating status, result, or the task slice.
func (m *BatchTaskManager) SnapshotBatchQueue(id string) (*BatchTaskQueue, bool) {
	if _, ok := m.GetBatchQueue(id); !ok {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	source, ok := m.queues[id]
	if !ok {
		return nil, false
	}
	queue := *source
	queue.NextRunAt = copyBatchTime(source.NextRunAt)
	queue.LastScheduleTriggerAt = copyBatchTime(source.LastScheduleTriggerAt)
	queue.StartedAt = copyBatchTime(source.StartedAt)
	queue.CompletedAt = copyBatchTime(source.CompletedAt)
	queue.Tasks = make([]*BatchTask, 0, len(source.Tasks))
	for _, item := range source.Tasks {
		if item == nil {
			continue
		}
		task := *item
		task.StartedAt = copyBatchTime(item.StartedAt)
		task.CompletedAt = copyBatchTime(item.CompletedAt)
		queue.Tasks = append(queue.Tasks, &task)
	}
	return &queue, true
}

func batchTaskCounts(queue *BatchTaskQueue) map[string]int {
	counts := map[string]int{"total": len(queue.Tasks), "pending": 0, "running": 0, "completed": 0, "failed": 0, "cancelled": 0}
	for _, task := range queue.Tasks {
		counts[task.Status]++
	}
	return counts
}

func batchTaskPreview(value string, limit int) string {
	count := 0
	for offset := range value {
		if count == limit {
			return value[:offset] + "…"
		}
		count++
	}
	return value
}

func (h *AgentHandler) writeBatchTaskPage(c *gin.Context, queue *BatchTaskQueue) {
	page, _ := strconv.Atoi(c.DefaultQuery("task_page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("task_page_size", "20"))
	if size < 1 || size > 100 {
		size = 20
	}
	if page < 1 {
		page = 1
	}
	total := len(queue.Tasks)
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	end := start + size
	if end > total {
		end = total
	}
	stats := batchTaskCounts(queue)
	tasks := queue.Tasks[start:end]
	for _, task := range tasks {
		task.Message = batchTaskPreview(task.Message, 600)
		task.Result = batchTaskPreview(task.Result, 400)
		task.Error = batchTaskPreview(task.Error, 400)
	}
	queue.Tasks = tasks
	c.JSON(http.StatusOK, gin.H{"queue": queue, "task_stats": stats, "task_pagination": gin.H{"page": page, "page_size": size, "total": total, "total_pages": pages, "offset": start, "preview": true}})
}

// GetBatchTask returns full evidence only for the selected task, within the
// already-authorized queue. List previews never replace the persisted result.
func (h *AgentHandler) GetBatchTask(c *gin.Context) {
	queue, ok := h.batchTaskManager.SnapshotBatchQueue(c.Param("queueId"))
	if !ok {
		c.JSON(404, gin.H{"error": "任务不存在"})
		return
	}
	for _, task := range queue.Tasks {
		if task.ID == c.Param("taskId") {
			stats := batchTaskCounts(queue)
			queue.Tasks = []*BatchTask{}
			c.JSON(200, gin.H{"task": task, "queue": queue, "task_stats": stats})
			return
		}
	}
	c.JSON(404, gin.H{"error": "子任务不存在"})
}
