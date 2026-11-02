// テスト用の偽のタイマー。`lib/ws.ts` の再接続は `window.setTimeout` で待つので、それを差し替えて、
// 待ち時間（ミリ秒）を記録し、テストが好きなときに 1 つずつ発火する。実時間では待たない。

interface Task {
  delay: number;
  callback: () => void;
}

export class FakeTimers {
  private nextId = 1;
  private readonly tasks = new Map<number, Task>();
  private original: { setTimeout: typeof window.setTimeout; clearTimeout: typeof window.clearTimeout } | null = null;

  install(): void {
    this.original = { setTimeout: window.setTimeout, clearTimeout: window.clearTimeout };
    window.setTimeout = ((callback: () => void, delay = 0) => {
      const id = this.nextId++;
      this.tasks.set(id, { delay, callback });
      return id;
    }) as unknown as typeof window.setTimeout;
    window.clearTimeout = ((id?: number) => {
      if (id !== undefined) {
        this.tasks.delete(id);
      }
    }) as typeof window.clearTimeout;
  }

  restore(): void {
    if (this.original) {
      window.setTimeout = this.original.setTimeout;
      window.clearTimeout = this.original.clearTimeout;
      this.original = null;
    }
    this.tasks.clear();
  }

  // 待機中のタイマーの待ち時間（登録順）。
  get pendingDelays(): number[] {
    return [...this.tasks.values()].map((task) => task.delay);
  }

  // fireNext は、最も古い待機中のタイマーを 1 つ発火し、その待ち時間を返す。無ければ例外。
  fireNext(): number {
    const first = this.tasks.entries().next();
    if (first.done) {
      throw new Error("no pending timer");
    }
    const [id, task] = first.value;
    this.tasks.delete(id);
    task.callback();
    return task.delay;
  }
}
