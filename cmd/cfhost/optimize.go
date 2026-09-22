package main

import (
	"context"
	"fmt"
	"time"
)

func (a *App) runFullOptimize(ctx context.Context, cfg Config) error {
	attempt:=time.Now()
	a.setJobProgress("准备完整优化", "记录优化状态", 1, 5, 0, 0)
	a.mu.Lock(); a.state.LastOptimizeAttempt=attempt.Format(time.RFC3339); a.mu.Unlock()
	a.markRefreshAttempt(attempt)
	a.setJobProgress("准备完整优化", "准备完成", 1, 5, 1, 1)
	a.setJobProgress("CFST 测速", "重新生成全局候选池", 2, 5, 0, 0)
	candidates,err:=a.runCFST(ctx,cfg)
	if err!=nil{return err}
	a.storeCandidates(candidates)
	a.setJobProgress("CFST 测速", fmt.Sprintf("得到 %d 个候选 IP", len(candidates)), 2, 5, 1, 1)
	a.setJobProgress("读取样本", "读取 Tracker 样本与下载器状态", 3, 5, 0, 0)
	samples:=a.loadSamples(ctx,cfg)
	a.setJobProgress("读取样本", "样本准备完成", 3, 5, 1, 1)

	a.mu.RLock(); current:=copyMappings(a.state.Mappings); health:=copyHealth(a.state.DomainHealth); a.mu.RUnlock()
	mappings:=make(map[string]string); statuses:=make(map[string]string); groupIP:=make(map[string]string)
	now:=time.Now().Format(time.RFC3339)

	for index,d:=range cfg.Domains {
		a.setJobDomainProgress("验证并选择域名", 4, 5, index+1, len(cfg.Domains), d.Host)
		if !d.Enabled { statuses[d.Host]="disabled"; continue }
		if d.Class=="normal" {
			order:=orderedCandidates(candidates,d,"","",cfg)
			h:=health[d.Host]
			if len(order)==0 {
				h.FailureStreak++;h.LastFailure=now;health[d.Host]=h
				statuses[d.Host]="normal optimize unresolved · no CFST candidate"
				continue
			}
			chosen:=order[0]
			mappings[d.Host]=chosen.IP
			statuses[d.Host]=fmt.Sprintf("normal optimized · lowest latency · %s · %.2f ms · verification skipped",chosen.IP,chosen.DelayMS)
			h.FailureStreak=0;h.LastSuccess=now;health[d.Host]=h
			a.appendLog("%s -> %s (normal full optimize · %.2f ms · verification skipped)",d.Host,chosen.IP,chosen.DelayMS)
			continue
		}
		if !domainRefreshable(d,cfg,samples) { statuses[d.Host]="tracker sample missing"; h:=health[d.Host];h.FailureStreak++;h.LastFailure=now;health[d.Host]=h;continue }
		preferred:=groupIP[groupKey(d)]
		order:=orderedCandidates(candidates,d,preferred,"",cfg)
		resolved:=false; lastDetail:="no candidate"
		for _,c:=range order {
			ok,detail:=a.verifyDomain(ctx,d,c.IP,cfg,samples);lastDetail=detail
			if !ok{continue}
			mappings[d.Host]=c.IP;statuses[d.Host]="optimized · "+c.IP+" · "+detail
			if k:=groupKey(d);k!=""&&groupIP[k]==""{groupIP[k]=c.IP}
			resolved=true;break
		}
		if !resolved {
			if oldIP:=current[d.Host];oldIP!="" {
				ok,detail:=a.verifyDomain(ctx,d,oldIP,cfg,samples)
				if ok { mappings[d.Host]=oldIP;statuses[d.Host]="retained after optimize · "+oldIP+" · "+detail;resolved=true }
			}
		}
		h:=health[d.Host]
		if resolved{h.FailureStreak=0;h.LastSuccess=now}else{h.FailureStreak++;h.LastFailure=now;statuses[d.Host]="optimize unresolved · "+lastDetail}
		health[d.Host]=h
	}
	a.setJobProgress("应用结果", fmt.Sprintf("写入 %d 个域名映射", len(mappings)), 5, 5, 0, 0)
	if err:=a.commitResolution(ctx,cfg,mappings,statuses,health,"",true);err!=nil{return err}
	a.completeJobProgress("完成", fmt.Sprintf("完整优化完成 · %d 个映射", len(mappings)))
	a.appendLog("full optimize completed: %d mappings",len(mappings))
	return nil
}
