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
	if err:=ctx.Err();err!=nil{return fmt.Errorf("full optimize aborted before mapping changes: %w",err)}
	a.storeCandidates(candidates)
	a.setJobProgress("CFST 测速", fmt.Sprintf("得到 %d 个候选 IP", len(candidates)), 2, 5, 1, 1)
	a.setJobProgress("读取样本", "读取 Tracker 样本与下载器状态", 3, 5, 0, 0)
	samples:=a.loadSamples(ctx,cfg)
	if err:=ctx.Err();err!=nil{return fmt.Errorf("full optimize aborted before mapping changes: %w",err)}
	a.setJobProgress("读取样本", "样本准备完成", 3, 5, 1, 1)

	a.mu.RLock(); current:=copyMappings(a.state.Mappings); health:=copyHealth(a.state.DomainHealth); a.mu.RUnlock()
	mappings:=retainEnabledMappings(current,cfg)
	statuses:=make(map[string]string)
	groupIP:=make(map[string]string)
	groupHardFailures:=make(map[string]map[string]bool)
	now:=time.Now().Format(time.RFC3339)

	for index,d:=range cfg.Domains {
		if err:=ctx.Err();err!=nil{return fmt.Errorf("full optimize aborted before commit: %w",err)}
		a.setJobDomainProgress("验证并选择域名", 4, 5, index+1, len(cfg.Domains), d.Host)
		if !d.Enabled { statuses[d.Host]="disabled"; delete(mappings,d.Host); continue }
		oldIP:=current[d.Host]
		h:=health[d.Host]
		if normalModeSkipsVerification(d) {
			order:=orderedCandidates(candidates,d,"","",cfg)
			if len(order)==0 {
				h.FailureStreak++;h.LastFailure=now;health[d.Host]=h
				if oldIP!="" {
					mappings[d.Host]=oldIP
					statuses[d.Host]="normal optimize unresolved · retained last-known-good · "+oldIP
				} else {
					statuses[d.Host]="normal optimize unresolved · no CFST candidate"
				}
				continue
			}
			chosen:=order[0]
			mappings[d.Host]=chosen.IP
			statuses[d.Host]=fmt.Sprintf("normal optimized · lowest latency · %s · %.2f ms · verification skipped",chosen.IP,chosen.DelayMS)
			h.FailureStreak=0;h.LastSuccess=now;health[d.Host]=h
			a.appendLog("%s -> %s (normal full optimize · %.2f ms · verification skipped)",d.Host,chosen.IP,chosen.DelayMS)
			continue
		}
		if !domainRefreshable(d,cfg,samples) {
			h.FailureStreak++;h.LastFailure=now;health[d.Host]=h
			if oldIP!="" {
				mappings[d.Host]=oldIP
				statuses[d.Host]="tracker sample missing · retained last-known-good · "+oldIP
			} else {
				statuses[d.Host]="tracker sample missing"
			}
			continue
		}

		domainCtx,cancel,budget,hasBudget:=domainVerificationContext(ctx,len(cfg.Domains)-index)
		if !hasBudget {
			h.FailureStreak++;h.LastFailure=now;health[d.Host]=h
			if oldIP!="" {
				mappings[d.Host]=oldIP
				statuses[d.Host]="optimize deferred · insufficient task budget · retained last-known-good · "+oldIP
			} else {
				statuses[d.Host]="optimize deferred · insufficient task budget"
			}
			continue
		}

		key:=groupKey(d)
		preferred:=groupIP[key]
		order:=orderedCandidates(candidates,d,preferred,"",cfg)
		resolved:=false
		lastDetail:="no candidate"
		oldHardFailed:=false
		tried:=make(map[string]bool)
		for _,candidate:=range order {
			if domainCtx.Err()!=nil {break}
			if groupHardFailed(groupHardFailures,key,candidate.IP){continue}
			tried[candidate.IP]=true
			ok,detail:=a.verifyDomain(domainCtx,d,candidate.IP,cfg,samples)
			lastDetail=detail
			if !ok {
				if verificationHardFailure(detail) {
					markGroupHardFailure(groupHardFailures,key,candidate.IP)
					if d.Mode=="http" && candidate.IP==oldIP {
						oldHardFailed=true
					}
				}
				continue
			}
			mappings[d.Host]=candidate.IP
			statuses[d.Host]="optimized · "+candidate.IP+" · "+detail
			if key!=""&&groupIP[key]==""{groupIP[key]=candidate.IP}
			resolved=true
			break
		}
		if !resolved && oldIP!="" && !tried[oldIP] && !groupHardFailed(groupHardFailures,key,oldIP) && domainCtx.Err()==nil {
			ok,detail:=a.verifyDomain(domainCtx,d,oldIP,cfg,samples)
			lastDetail=detail
			if ok {
				mappings[d.Host]=oldIP
				statuses[d.Host]="retained after optimize · "+oldIP+" · "+detail
				resolved=true
			} else if verificationHardFailure(detail) {
				markGroupHardFailure(groupHardFailures,key,oldIP)
				if d.Mode=="http" {
					oldHardFailed=true
				}
			}
		}
		domainErr:=domainCtx.Err()
		cancel()
		if !resolved {
			if domainErr==context.DeadlineExceeded && ctx.Err()==nil {
				lastDetail=fmt.Sprintf("domain verification budget exceeded · %s",budget.Round(time.Second))
				a.appendLog("%s optimize verification budget exhausted after %s; continuing",d.Host,budget.Round(time.Second))
			}
			if oldIP!="" && !oldHardFailed {
				mappings[d.Host]=oldIP
				statuses[d.Host]="optimize unresolved · "+lastDetail+" · retained last-known-good · "+oldIP
			} else {
				delete(mappings,d.Host)
				if oldIP!="" && oldHardFailed {
					statuses[d.Host]="optimize unresolved · confirmed HTTP hard failure · old mapping removed · "+oldIP+" · "+lastDetail
				} else {
					statuses[d.Host]="optimize unresolved · "+lastDetail
				}
			}
			h.FailureStreak++;h.LastFailure=now
		} else {
			h.FailureStreak=0;h.LastSuccess=now
		}
		health[d.Host]=h
	}
	if err:=ctx.Err();err!=nil{return fmt.Errorf("full optimize aborted before commit: %w",err)}
	a.setJobProgress("应用结果", fmt.Sprintf("提交 %d 个域名映射", len(mappings)), 5, 5, 0, 0)
	if err:=a.commitResolution(ctx,cfg,mappings,statuses,health,"",true);err!=nil{return err}
	a.completeJobProgress("完成", fmt.Sprintf("完整优化完成 · %d 个映射", len(mappings)))
	a.appendLog("full optimize completed: %d mappings",len(mappings))
	return nil
}
