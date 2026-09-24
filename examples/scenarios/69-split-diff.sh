# shellcheck shell=bash disable=SC2154
# 69. split-diff — one file long enough for the split view's full-file mode to
# mean something. Its working-tree edits are spread out, each a different
# shape: a line changed in place (word marks), lines inserted (hatched filler
# on the left), lines removed (hatched filler on the right), and a replaced
# block of unequal length. Nested code, so the indent guides have levels to
# draw. Open queue.ts's diff in split view.
R="$ROOT/split-diff"
new_repo "$R"

cat > "$R/queue.ts" <<'EOF'
// A small retrying job queue.

export interface Job {
  id: string
  attempts: number
  run: () => Promise<void>
}

export interface QueueOptions {
  concurrency: number
  maxAttempts: number
  backoffMs: number
}

const DEFAULTS: QueueOptions = {
  concurrency: 2,
  maxAttempts: 3,
  backoffMs: 250
}

export class Queue {
  private pending: Job[] = []
  private running = 0
  private readonly options: QueueOptions

  constructor(options: Partial<QueueOptions> = {}) {
    this.options = { ...DEFAULTS, ...options }
  }

  push(job: Job): void {
    this.pending.push(job)
    this.drain()
  }

  get size(): number {
    return this.pending.length + this.running
  }

  private drain(): void {
    while (this.running < this.options.concurrency && this.pending.length > 0) {
      const job = this.pending.shift()
      if (!job) return
      this.running++
      void this.execute(job)
    }
  }

  private async execute(job: Job): Promise<void> {
    try {
      await job.run()
    } catch (err) {
      job.attempts++
      if (job.attempts < this.options.maxAttempts) {
        await sleep(this.options.backoffMs)
        this.pending.push(job)
      } else {
        console.error(`job ${job.id} failed`, err)
      }
    } finally {
      this.running--
      this.drain()
    }
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
EOF
git -C "$R" add -A && git -C "$R" commit -qm "queue: retrying job queue"

cat > "$R/queue.ts" <<'EOF'
// A small retrying job queue.

export interface Job {
  id: string
  attempts: number
  run: () => Promise<void>
}

export interface QueueOptions {
  concurrency: number
  maxAttempts: number
  backoffMs: number
  onFailure?: (job: Job, err: unknown) => void
}

const DEFAULTS: QueueOptions = {
  concurrency: 4,
  maxAttempts: 3,
  backoffMs: 250
}

export class Queue {
  private pending: Job[] = []
  private running = 0
  private readonly options: QueueOptions

  constructor(options: Partial<QueueOptions> = {}) {
    this.options = { ...DEFAULTS, ...options }
  }

  push(job: Job): void {
    this.pending.push(job)
    this.drain()
  }

  private drain(): void {
    while (this.running < this.options.concurrency && this.pending.length > 0) {
      const job = this.pending.shift()
      if (!job) return
      this.running++
      void this.execute(job)
    }
  }

  private async execute(job: Job): Promise<void> {
    try {
      await job.run()
    } catch (err) {
      job.attempts++
      if (job.attempts < this.options.maxAttempts) {
        // Exponential: 250ms, 500ms, 1s, …
        await sleep(this.options.backoffMs * 2 ** (job.attempts - 1))
        this.pending.push(job)
      } else if (this.options.onFailure) {
        this.options.onFailure(job, err)
      } else {
        console.error(`job ${job.id} failed after ${job.attempts} attempts`, err)
      }
    } finally {
      this.running--
      this.drain()
    }
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
EOF

summary "split-diff" "split view, full file: open queue.ts's diff in split view — edits spread through the file, inserted and removed lines hatched on the other side"
