package handlers

import "testing"

func TestEnqueueBatchKeepsRunningBatchAndQueuesFollowingBatches(t *testing.T) {
	handler := &BatchHandler{
		tasks:          []BatchTask{{ID: "video-current", Status: "downloading"}},
		currentBatchID: "batch-current",
		running:        true,
	}

	oldTasks, position := handler.enqueueBatch(batchQueueItem{
		ID:    "batch-second",
		Tasks: []BatchTask{{ID: "video-second", Status: "pending"}},
	})
	if len(oldTasks) != 0 {
		t.Fatalf("enqueueBatch() old tasks = %d, want 0 for queued batch", len(oldTasks))
	}
	if position != 1 {
		t.Fatalf("enqueueBatch() queue position = %d, want 1", position)
	}
	if handler.currentBatchID != "batch-current" || handler.tasks[0].ID != "video-current" {
		t.Fatalf("running batch was replaced: current=%q task=%q", handler.currentBatchID, handler.tasks[0].ID)
	}

	_, position = handler.enqueueBatch(batchQueueItem{
		ID:    "batch-third",
		Tasks: []BatchTask{{ID: "video-third", Status: "pending"}},
	})
	if position != 2 {
		t.Fatalf("third batch queue position = %d, want 2", position)
	}
	if len(handler.pendingBatches) != 2 {
		t.Fatalf("pending batches = %d, want 2", len(handler.pendingBatches))
	}
}

func TestPromoteNextBatchUsesFIFOOrder(t *testing.T) {
	handler := &BatchHandler{
		pendingBatches: []batchQueueItem{
			{ID: "batch-second", Tasks: []BatchTask{{ID: "video-second", Status: "pending"}}},
			{ID: "batch-third", Tasks: []BatchTask{{ID: "video-third", Status: "pending"}}},
		},
	}

	next, ok := handler.promoteNextBatchLocked()
	if !ok {
		t.Fatal("promoteNextBatchLocked() did not promote a queued batch")
	}
	if next.ID != "batch-second" || handler.currentBatchID != "batch-second" {
		t.Fatalf("promoted batch = %q, current = %q, want batch-second", next.ID, handler.currentBatchID)
	}
	if !handler.running || handler.tasks[0].ID != "video-second" {
		t.Fatalf("promoted batch was not activated: running=%v tasks=%v", handler.running, handler.tasks)
	}
	if len(handler.pendingBatches) != 1 || handler.pendingBatches[0].ID != "batch-third" {
		t.Fatalf("remaining queue = %#v, want batch-third", handler.pendingBatches)
	}
}

func TestEnqueueBatchStartsImmediatelyAfterTerminalBatch(t *testing.T) {
	handler := &BatchHandler{
		tasks:          []BatchTask{{ID: "video-old", Status: "done"}},
		currentBatchID: "batch-old",
	}

	oldTasks, position := handler.enqueueBatch(batchQueueItem{
		ID:    "batch-new",
		Tasks: []BatchTask{{ID: "video-new", Status: "pending"}},
	})
	if position != 0 {
		t.Fatalf("queue position = %d, want immediate start", position)
	}
	if len(oldTasks) != 1 || oldTasks[0].ID != "video-old" {
		t.Fatalf("old tasks = %#v, want terminal task snapshot", oldTasks)
	}
	if handler.currentBatchID != "batch-new" || !handler.running {
		t.Fatalf("new batch was not started: current=%q running=%v", handler.currentBatchID, handler.running)
	}
}
