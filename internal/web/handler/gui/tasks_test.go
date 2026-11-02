package gui_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

// このファイルはエージェントのタスクの画面（M4。FR-H10・FR-H02）のテスト。CSP は rig が全 HTML 応答で検査する。

const (
	idRunning  = "01JD0000000000000000RUN001"
	idFinished = "01JD0000000000000000FIN002"
)

// seedTask はタスクを入れる。時計の 12:00 UTC から minutes 分前に作られた、指示 prompt の status のタスク。
func seedTask(f *guitest.Fakes, id string, status domain.TaskStatus, prompt string, minutesAgo int, mutate ...func(*domain.Task)) *domain.Task {
	created := f.Clock.T.Add(-time.Duration(minutesAgo) * time.Minute)
	t := domain.Task{
		ID: id, ClientID: "c1", ClientLabel: "claude-desktop", Prompt: prompt, Status: status, Model: "gpt-6.1-sol",
		MaxSteps: 50, MaxDurationSec: 600, CreatedAt: created,
	}
	if status != domain.TaskPendingApproval && status != domain.TaskDenied {
		started := created.Add(2 * time.Second)
		t.StartedAt = &started
	}
	if status.Terminal() {
		fin := created.Add(95 * time.Second)
		t.FinishedAt = &fin
	}
	for _, m := range mutate {
		m(&t)
	}
	f.Tasks.Items = append(f.Tasks.Items, t)
	return &f.Tasks.Items[len(f.Tasks.Items)-1]
}

func withSteps(fast, openai int) func(*domain.Task) {
	return func(t *domain.Task) { t.FastSteps, t.OpenAISteps, t.StepCount = fast, openai, fast+openai }
}

func TestTasksListPage(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedTask(f, idFinished, domain.TaskCompleted, "メモ帳を開いて\n「こんにちは」と書く", 30, withSteps(1, 2))
		seedTask(f, idRunning, domain.TaskRunning, "ブラウザで天気を調べる", 1, withSteps(0, 3))
		seedTask(f, "01JD0000000000000000FAI003", domain.TaskFailed, "x", 60, func(t *domain.Task) { t.ErrorCode = "judge_denied"; t.ClientLabel = ""; t.ClientID = "" })
	})
	res := r.get("/tasks")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", "<title>タスク - puppet-remo</title>", `aria-current="page"`, `id="task-list"`,
		// 新しい順（実行中 → 完了 → 失敗）、状態バッジ、ステップ数 / 上限、依頼元（削除済みはその旨）、詳細へのリンク
		"00RUN001", "00FIN002", `href="/tasks/`+idRunning+`"`, `data-status="running"`, `data-status="completed"`, `data-status="failed"`,
		"実行中", "完了", "失敗", "3 / 50", "claude-desktop", "（削除済み）",
		// 指示は改行をたたんで 1 行に
		"メモ帳を開いて 「こんにちは」と書く", "ブラウザで天気を調べる")
	body := res.Body
	if i, j, k := strings.Index(body, "00RUN001"), strings.Index(body, "00FIN002"), strings.Index(body, "00FAI003"); i >= j || j >= k {
		t.Errorf("tasks are not newest first:\n%s", body)
	}
	lacks(t, res, "data-pager-next", `data-empty="task-list"`)
	if r.f.Tasks.LastQuery.Cursor != "" || r.f.Tasks.LastQuery.Limit != 0 {
		t.Errorf("List query = %+v, want the first page with the default limit", r.f.Tasks.LastQuery)
	}
}

func TestTasksListEmpty(t *testing.T) {
	res := newRig(t).get("/tasks")
	status(t, res, http.StatusOK)
	contains(t, res, `data-empty="task-list"`, "まだありません")
	lacks(t, res, "<table", "data-pager-next")
}

// 50 件ずつ: 51 件あれば「次へ」が出て、cursor で残りが出る（フラグメントは一覧だけ）。
func TestTasksListPaging(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		for i := range 51 {
			seedTask(f, fmt.Sprintf("01JD00000000000000000T%04d", i), domain.TaskCompleted, fmt.Sprintf("task-prompt-%02d", i), i)
		}
	})
	first := r.get("/tasks")
	status(t, first, http.StatusOK)
	if n := strings.Count(first.Body, "data-task-row="); n != 50 {
		t.Fatalf("first page rows = %d, want 50", n)
	}
	contains(t, first, "data-pager-next", `hx-target="#task-list"`, `hx-push-url="true"`, "task-prompt-00")
	lacks(t, first, "task-prompt-50")
	m := strings.Index(first.Body, `href="/tasks?cursor=`)
	if m < 0 {
		t.Fatalf("no next link:\n%s", first.Body)
	}
	next := first.Body[m+len(`href="`):]
	next = html2url(next[:strings.Index(next, `"`)])

	second := r.get(next, hx...)
	status(t, second, http.StatusOK)
	contains(t, second, `id="task-list"`, "task-prompt-50")
	lacks(t, second, "<html", "task-prompt-00", "data-pager-next")
	if n := strings.Count(second.Body, "data-task-row="); n != 1 {
		t.Errorf("second page rows = %d, want 1", n)
	}
	if r.f.Tasks.LastQuery.Cursor == "" {
		t.Error("the cursor was not passed to the service")
	}
}

func html2url(s string) string { return strings.NewReplacer("&amp;", "&", "&#43;", "+").Replace(s) }

func TestTasksListHTMXForms(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedTask(f, idRunning, domain.TaskRunning, "p", 1) })
	t.Run("boosted", func(t *testing.T) {
		res := r.get("/tasks", "HX-Request", "true", "HX-Boosted", "true")
		contains(t, res, "<title>タスク - puppet-remo</title>", `id="sidebar-nav"`, `hx-swap-oob="true"`, `id="task-list"`)
		lacks(t, res, "<html", "<body")
	})
	t.Run("fragment", func(t *testing.T) {
		res := r.get("/tasks", hx...)
		contains(t, res, `id="task-list"`)
		lacks(t, res, "<html", `id="sidebar-nav"`)
	})
	t.Run("history restore gets the full page", func(t *testing.T) {
		contains(t, r.get("/tasks", "HX-Request", "true", "HX-History-Restore-Request", "true"), "<html")
	})
}

func TestTasksListErrors(t *testing.T) {
	t.Run("invalid cursor is 422", func(t *testing.T) {
		res := newRig(t).get("/tasks?cursor=garbage")
		status(t, res, http.StatusUnprocessableEntity)
		contains(t, res, `data-error-code="invalid_argument"`)
	})
	t.Run("a read failure is a 500 without the internal message", func(t *testing.T) {
		res := newRig(t, func(f *guitest.Fakes) { f.Tasks.ListErr = errors.New("db: disk I/O error at C:\\secret\\x.db") }).get("/tasks")
		status(t, res, http.StatusInternalServerError)
		contains(t, res, `data-error-code="internal"`)
		lacks(t, res, "disk I/O", "secret")
	})
}

func TestTaskDetailPage(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedTask(f, idFinished, domain.TaskCompleted, "メモ帳を開いて「こんにちは」と書く", 30, withSteps(1, 2), func(t *domain.Task) {
			t.HandoffReason = domain.HandoffLowConfidence
			t.InputTokens, t.OutputTokens = 12345, 678
			t.ResultText = "メモ帳に書きました。"
		})
		f.Tasks.Steps = map[string][]service.TaskStep{idFinished: {
			{No: 2, Route: "openai", Event: "agent.step", Result: "ok", Summary: "クリック (120, 80)", AuditID: 902, Thumb: true,
				Args: []service.AuditArg{{Key: "x", Value: "120"}}, At: f.Clock.T},
			{No: 1, Route: "fast", Event: "agent.step", Result: "ok", Summary: "要素「メモ帳」をクリック", AuditID: 901, At: f.Clock.T},
			{No: 3, Route: "openai", Event: "agent.step", Result: "denied", Summary: "文字を入力（本文は記録しない）", At: f.Clock.T},
		}}
	})
	res := r.get("/tasks/" + idFinished)
	status(t, res, http.StatusOK)
	contains(t, res, "<html", "<title>タスクの詳細 - puppet-remo</title>", `data-task-detail="`+idFinished+`"`,
		"メモ帳を開いて「こんにちは」と書く", "claude-desktop", "gpt-6.1-sol", "ステップ 50 ・ 600 秒",
		// step_count = fast + openai
		"3（高速パス 1 ・ OpenAI 2）", `data-task-steps>3 / 50<`,
		"確信度がしきい値に届かなかった（low_confidence）", "12345", "678",
		// 完了はモデルの申告（FR-G13）
		"メモ帳に書きました。", "data-task-result-note", "モデル（OpenAI）の申告",
		// ステップは step_no の昇順、経路、結果、監査ログとサムネイル（監査の画面と同じ経路）
		`data-step-no="1" data-route="fast"`, `data-step-no="2" data-route="openai"`, "高速パス", "OpenAI",
		`href="/audit/901"`, `href="/audit/902"`, `src="/audit/902/thumbnail"`, "x:")
	lacks(t, res, `src="/audit/901/thumbnail"`, `hx-post=`, "中断")
	if i, j, k := strings.Index(res.Body, `data-step-no="1"`), strings.Index(res.Body, `data-step-no="2"`), strings.Index(res.Body, `data-step-no="3"`); i >= j || j >= k {
		t.Error("steps are not in step_no order")
	}
}

func TestTaskDetailStates(t *testing.T) {
	tests := []struct {
		name   string
		status domain.TaskStatus
		mutate func(*domain.Task)
		want   []string
		not    []string
	}{
		{"running offers cancel", domain.TaskRunning, nil, []string{`hx-post="/tasks/` + idRunning + `/cancel"`, "実行中", `closest [data-task-status]`}, []string{"data-task-result-note", "エラーコード"}},
		{"pending approval offers cancel", domain.TaskPendingApproval, nil, []string{`hx-post="/tasks/` + idRunning + `/cancel"`, "承認待ち"}, nil},
		{"failed shows the error code and no cancel", domain.TaskFailed, func(t *domain.Task) { t.ErrorCode = "safety_check_denied" },
			[]string{"失敗", "safety_check_denied", "OpenAI の安全チェックをホストが拒否した"}, []string{"hx-post", "data-task-result-note"}},
		{"unknown error code is shown as is", domain.TaskFailed, func(t *domain.Task) { t.ErrorCode = "something_new" }, []string{"something_new"}, []string{"data-task-field=\"error-label\""}},
		{"limit reached", domain.TaskLimitReached, nil, []string{"上限に到達"}, []string{"hx-post"}},
		{"cancelled", domain.TaskCancelled, nil, []string{"中断</span>"}, []string{"hx-post"}},
		{"aborted", domain.TaskAborted, nil, []string{"異常終了"}, []string{"hx-post"}},
		{"denied", domain.TaskDenied, func(t *domain.Task) { t.ErrorCode = "approval_denied" }, []string{"承認が拒否された", "approval_denied"}, []string{"hx-post"}},
		// completed でも最終回答が無ければ注意書きは出さない。
		{"completed without a result", domain.TaskCompleted, nil, []string{`data-empty="task-result"`}, []string{"data-task-result-note"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, func(f *guitest.Fakes) {
				var m []func(*domain.Task)
				if tt.mutate != nil {
					m = append(m, tt.mutate)
				}
				seedTask(f, idRunning, tt.status, "p", 5, m...)
			})
			res := r.get("/tasks/" + idRunning)
			status(t, res, http.StatusOK)
			contains(t, res, tt.want...)
			lacks(t, res, tt.not...)
		})
	}
}

func TestTaskDetailNoHandoffNoSteps(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedTask(f, idRunning, domain.TaskRunning, "p", 1) })
	res := r.get("/tasks/" + idRunning)
	contains(t, res, `data-empty="task-steps"`, "記録されたステップはありません", `data-empty="task-result"`)
	lacks(t, res, "引き継ぎ理由", "<table")
}

func TestTaskDetailNotFoundAndHTMX(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedTask(f, idRunning, domain.TaskRunning, "p", 1) })
	res := r.get("/tasks/nope")
	status(t, res, http.StatusNotFound)
	contains(t, res, "<html", `data-error-code="not_found"`, "ページが見つかりません")
	res = r.get("/tasks/nope", hx...)
	status(t, res, http.StatusNotFound)
	contains(t, res, `data-error-code="not_found"`)
	lacks(t, res, "<html")
	res = r.get("/tasks/"+idRunning, "HX-Request", "true", "HX-Boosted", "true")
	status(t, res, http.StatusOK)
	contains(t, res, "<title>タスクの詳細 - puppet-remo</title>", `id="sidebar-nav"`)
	lacks(t, res, "<html")
}

// 指示・最終回答・クライアント名・ステップの文字列は HTML エスケープされる。
func TestTaskPagesEscapeUntrustedText(t *testing.T) {
	const xss = `<script>alert(1)</script><img src=x onerror=alert(2)>`
	r := newRig(t, func(f *guitest.Fakes) {
		seedTask(f, idRunning, domain.TaskCompleted, xss, 5, func(t *domain.Task) {
			t.ResultText = xss
			t.ClientLabel = xss
			t.ErrorCode = xss
			t.HandoffReason = domain.HandoffReason(xss)
		})
		f.Tasks.Steps = map[string][]service.TaskStep{idRunning: {
			{No: 1, Route: "fast", Event: xss, Result: xss, Summary: xss, Args: []service.AuditArg{{Key: xss, Value: xss}}, At: f.Clock.T},
		}}
	})
	for _, target := range []string{"/tasks", "/tasks/" + idRunning, "/"} {
		res := r.get(target)
		status(t, res, http.StatusOK)
		lacks(t, res, "<script>alert", "<img src=x", "onerror=alert(2)>")
		if target != "/" {
			contains(t, res, "&lt;script&gt;alert(1)&lt;/script&gt;")
		}
	}
}

// 指示の先頭だけを出す（80 文字を超えたら末尾を「…」に）。詳細には全文を出す。
func TestTaskPromptExcerpt(t *testing.T) {
	long := strings.Repeat("あ", 200)
	r := newRig(t, func(f *guitest.Fakes) { seedTask(f, idRunning, domain.TaskRunning, long, 1) })
	res := r.get("/tasks")
	contains(t, res, strings.Repeat("あ", 79)+"…")
	lacks(t, res, strings.Repeat("あ", 80))
	contains(t, r.get("/tasks/"+idRunning), long)
	contains(t, r.get("/"), strings.Repeat("あ", 79)+"…")
}

func TestCancelTask(t *testing.T) {
	t.Run("running is cancelled and returns the status line with a toast", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) { seedTask(f, idRunning, domain.TaskRunning, "p", 1, withSteps(0, 4)) })
		res := r.post("/tasks/"+idRunning+"/cancel", nil, hx...)
		status(t, res, http.StatusOK)
		contains(t, res, `data-task-status="cancelled"`, "中断</span>", "4 / 50", "タスクを中断しました。", `hx-swap-oob="beforeend:#toast-root"`)
		lacks(t, res, "<html", "/cancel", "hx-post")
		if got := r.f.Tasks.CancelCalls; len(got) != 1 || got[0] != idRunning {
			t.Errorf("CancelByHost calls = %v", got)
		}
		// 二度目は終了済みなので冪等（状態はそのまま。中断済みと分かる）。
		res = r.post("/tasks/"+idRunning+"/cancel", nil, hx...)
		status(t, res, http.StatusOK)
		contains(t, res, `data-task-status="cancelled"`)
		lacks(t, res, "hx-post")
	})
	t.Run("an already finished task returns its current state", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) { seedTask(f, idFinished, domain.TaskCompleted, "p", 10, withSteps(1, 1)) })
		res := r.post("/tasks/"+idFinished+"/cancel", nil, hx...)
		status(t, res, http.StatusOK)
		contains(t, res, `data-task-status="completed"`, "完了", "2 / 50", "すでに終了しています")
		lacks(t, res, "タスクを中断しました。", "hx-post")
		if got := r.f.Tasks.Items[0].Status; got != domain.TaskCompleted {
			t.Errorf("status changed to %q", got)
		}
	})
	t.Run("pending approval can be cancelled", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) { seedTask(f, idRunning, domain.TaskPendingApproval, "p", 1) })
		res := r.post("/tasks/"+idRunning+"/cancel", nil, hx...)
		status(t, res, http.StatusOK)
		contains(t, res, `data-task-status="cancelled"`)
	})
	t.Run("unknown id is the usual 404 error fragment", func(t *testing.T) {
		r := newRig(t)
		res := r.post("/tasks/nope/cancel", nil, hx...)
		status(t, res, http.StatusNotFound)
		contains(t, res, `data-error-code="not_found"`, `role="alert"`)
		lacks(t, res, "<html")
		if res.Header.Get("HX-Retarget") != "#toast-root" || res.Header.Get("HX-Reswap") != "beforeend" {
			t.Errorf("retarget headers = %q / %q", res.Header.Get("HX-Retarget"), res.Header.Get("HX-Reswap"))
		}
	})
	t.Run("a service failure is a 500 without the internal message", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) {
			seedTask(f, idRunning, domain.TaskRunning, "p", 1)
			f.Tasks.CancelErr = errors.New("agent: lease release failed: C:\\secret")
		})
		res := r.post("/tasks/"+idRunning+"/cancel", nil, hx...)
		status(t, res, http.StatusInternalServerError)
		contains(t, res, `data-error-code="internal"`)
		lacks(t, res, "secret", "lease release")
	})
}

// ダッシュボードの実行中タスク（FR-H02）: 実行中・承認待ちがあるときだけ、パネルに ID・依頼元・指示の先頭・状態・ステップ・経過・中断を出す。
func TestDashboardShowsTheRunningTask(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedTask(f, idRunning, domain.TaskRunning, "ブラウザで天気を調べる", 2, withSteps(1, 3), func(t *domain.Task) {
			started := t.CreatedAt.Add(time.Second)
			t.StartedAt = &started
		})
		seedTask(f, idFinished, domain.TaskCompleted, "finished-prompt", 30)
	})
	res := r.get("/")
	status(t, res, http.StatusOK)
	contains(t, res, `id="running-tasks"`, "実行中のエージェントタスク", "data-running-task", "00RUN001", "claude-desktop",
		"ブラウザで天気を調べる", "実行中", "4 / 50", "1分59秒",
		`hx-post="/tasks/`+idRunning+`/cancel"`, `hx-target="closest [data-task-status]"`, `href="/tasks/`+idRunning+`"`, `name="rev_tasks"`)
	// 終了済みのタスクは出さない。
	lacks(t, res, "finished-prompt", "00FIN002")
}

func TestDashboardRunningTaskPendingApprovalAndNone(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedTask(f, idRunning, domain.TaskPendingApproval, "p", 1) })
	contains(t, r.get("/"), "承認待ち", `hx-post="/tasks/`+idRunning+`/cancel"`)
	none := newRig(t).get("/")
	lacks(t, none, "data-running-task", "実行中のエージェントタスク", "/cancel")
	contains(t, none, `id="running-tasks"`, `name="rev_tasks"`)
}

// Running が失敗してもダッシュボードは出る（パネルだけ空）。
func TestDashboardSurvivesARunningTasksFailure(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Tasks.RunningErr = errors.New("db down") })
	res := r.get("/")
	status(t, res, http.StatusOK)
	lacks(t, res, "data-running-task", "db down")
}

// GET /poll: rev_tasks を送ったとき（ダッシュボードを表示中）だけ、版が違えば #running-tasks を差し替える。
func TestPollSwapsTheRunningTasksPanel(t *testing.T) {
	r := newRig(t)
	empty := r.get("/")
	rev := revOf(t, empty, "rev_tasks")

	// 版が同じなら差し替えない。表示していない（rev_tasks 無し）なら送らない。
	same := r.get(pollURL(url.Values{"rev_tasks": {rev}}), hx...)
	lacks(t, same, "running-tasks")
	seedTask(r.f, idRunning, domain.TaskRunning, "ブラウザで天気を調べる", 1, withSteps(0, 1))
	lacks(t, r.get(pollURL(nil), hx...), "running-tasks")

	// タスクが始まった → 版が違うので OOB で差し替える（中断のボタン付き）。
	res := r.get(pollURL(url.Values{"rev_tasks": {rev}}), hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="running-tasks"`, `hx-swap-oob="true"`, "ブラウザで天気を調べる", `hx-post="/tasks/`+idRunning+`/cancel"`, `name="rev_tasks"`)
	lacks(t, res, "<html")

	// 中断すると、パネルは空に戻る（中身を出さない）。
	r.post("/tasks/"+idRunning+"/cancel", nil, hx...)
	rev2 := revOf(t, res, "rev_tasks")
	res = r.get(pollURL(url.Values{"rev_tasks": {rev2}}), hx...)
	contains(t, res, `id="running-tasks"`, `hx-swap-oob="true"`)
	lacks(t, res, "data-running-task", "ブラウザで天気を調べる")
	if revOf(t, res, "rev_tasks") != rev {
		t.Error("the empty panel must have the same rev as before")
	}
}

// 強制剥奪が Revoke を呼ぶ経路は agent でも同じ（host として）。
func TestRevokeAgentLease(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Leases.Held = true
		f.Leases.H = service.Holder{LeaseID: "l1", Actor: domain.Actor{Kind: domain.ActorAgent, ClientID: "c1", Label: "claude-desktop", SessionID: idRunning}, Since: f.Clock.T, IdleRemaining: time.Minute}
	})
	res := r.post("/lease/revoke", nil, hx...)
	status(t, res, http.StatusOK)
	must(t, r.f.Leases.RevokeCalls == 1 && r.f.Leases.LastBy == service.HostActor, "Revoke calls=%d by=%+v", r.f.Leases.RevokeCalls, r.f.Leases.LastBy)
	contains(t, res, "空き", "操作権を剥奪しました。")
}

func TestSidebarHasTheTasksEntry(t *testing.T) {
	res := newRig(t).get("/")
	contains(t, res, `href="/tasks"`, ">タスク<")
	contains(t, newRig(t).get("/tasks"), `aria-current="page"`)
}
