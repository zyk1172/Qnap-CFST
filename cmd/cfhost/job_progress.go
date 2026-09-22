package main

import "fmt"

type JobProgress struct {
	Stage   string `json:"stage,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Step    int    `json:"step,omitempty"`
	Steps   int    `json:"steps,omitempty"`
	Current int    `json:"current,omitempty"`
	Total   int    `json:"total,omitempty"`
	Percent int    `json:"percent,omitempty"`
}

func calculateJobProgress(step, steps, current, total int) int {
	if steps <= 0 {
		return 0
	}
	if step < 1 {
		step = 1
	}
	if step > steps {
		step = steps
	}
	progress := float64(step-1) / float64(steps)
	if total > 0 {
		if current < 0 {
			current = 0
		}
		if current > total {
			current = total
		}
		progress += (float64(current) / float64(total)) / float64(steps)
	}
	percent := int(progress*100 + 0.5)
	if percent < 0 {
		return 0
	}
	if percent > 99 {
		return 99
	}
	return percent
}

func newJobProgress(stage, detail string, step, steps, current, total int) JobProgress {
	return JobProgress{
		Stage:   stage,
		Detail:  detail,
		Step:    step,
		Steps:   steps,
		Current: current,
		Total:   total,
		Percent: calculateJobProgress(step, steps, current, total),
	}
}

func initialJobProgress(kind string) JobProgress {
	switch kind {
	case "repair":
		return newJobProgress("准备 Repair", "初始化任务", 1, 7, 0, 0)
	case "optimize":
		return newJobProgress("准备完整优化", "初始化任务", 1, 5, 0, 0)
	case "run":
		return newJobProgress("准备测速", "初始化 CFST", 1, 3, 0, 0)
	case "maintain":
		return newJobProgress("准备单域名维护", "初始化任务", 1, 6, 0, 0)
	default:
		return newJobProgress("准备任务", kind, 1, 1, 0, 0)
	}
}

func (a *App) setJobProgress(stage, detail string, step, steps, current, total int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.state.Running {
		return
	}
	a.state.Progress = newJobProgress(stage, detail, step, steps, current, total)
}

func (a *App) setJobDomainProgress(stage string, step, steps, current, total int, host string) {
	detail := host
	if total > 0 {
		detail = fmt.Sprintf("%d/%d · %s", current, total, host)
	}
	a.setJobProgress(stage, detail, step, steps, current, total)
}

func (a *App) completeJobProgress(stage, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.state.Running {
		return
	}
	p := a.state.Progress
	if p.Steps <= 0 {
		p.Steps = 1
	}
	p.Stage = stage
	p.Detail = detail
	p.Step = p.Steps
	p.Current = 1
	p.Total = 1
	p.Percent = 100
	a.state.Progress = p
}
