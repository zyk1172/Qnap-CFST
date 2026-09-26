package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

type pendingDomain struct {
	Domain        Domain
	FailedCurrent string
	Refreshable   bool
}

func (a *App) runJob(ctx context.Context, kind string, cfg Config) error {
	switch kind {
	case "run":
		a.setJobProgress("准备测速", "准备 CFST 参数", 1, 3, 1, 1)
		a.markRefreshAttempt(time.Now())
		a.setJobProgress("CFST 测速", "正在测试候选 IP", 2, 3, 0, 0)
		candidates, err := a.runCFST(ctx, cfg)
		if err != nil { return err }
		a.setJobProgress("保存候选", fmt.Sprintf("写入 %d 个候选 IP", len(candidates)), 3, 3, 0, 0)
		a.storeCandidates(candidates)
		a.completeJobProgress("完成", fmt.Sprintf("得到 %d 个候选 IP", len(candidates)))
		return nil
	case "repair":
		return a.runSmartRepair(ctx, cfg)
	case "optimize":
		return a.runFullOptimize(ctx, cfg)
	default:
		return fmt.Errorf("unknown job: %s", kind)
	}
}

func (a *App) loadSamples(ctx context.Context,cfg Config) map[string]TrackerSample {
	samples:=map[string]TrackerSample{}
	if !cfg.Tracker.RealAnnounce {
		a.refreshTrackerSampleInventory(cfg)
		return samples
	}

	manual,err:=loadTrackerSamples(cfg.Tracker.SamplesPath)
	if manual!=nil { mergeTrackerSamples(samples,manual,true) }
	if err!=nil && !os.IsNotExist(err) { a.appendLog("tracker manual samples warning: %v",err) }

	autoCache,autoErr:=loadTrackerSamples(cfg.Tracker.AutoSamplesPath)
	if autoCache!=nil { mergeTrackerSamples(samples,autoCache,false) }
	if autoErr!=nil && !os.IsNotExist(autoErr) { a.appendLog("tracker auto samples warning: %v",autoErr) }

	autoSamples:=autoCache
	discoveredDomains:=[]string{}
	if cfg.Tracker.AutoDiscover && (cfg.Tracker.Transmission.Enabled || cfg.Tracker.QBittorrent.Enabled) {
		cache,report:=a.refreshAutoTrackerSamples(ctx,cfg)
		autoSamples=cache
		discoveredDomains=append(discoveredDomains,report.Domains...)
		for _,msg:=range report.Errors { a.appendLog("tracker auto-discovery: %s",msg) }
		if len(report.Domains)>0 {
			a.appendLog("tracker auto-discovery: transmission=%d qbittorrent=%d domains=%d",report.Transmission,report.QBittorrent,len(report.Domains))
		}
		mergeTrackerSamples(samples,cache,false)
	}

	// Manually managed samples always take precedence over discovered/cache samples.
	if manual!=nil { mergeTrackerSamples(samples,manual,true) }

	// Normal Tracker domains explicitly opt out of sample/Transmission
	// monitoring. Ignore stale/manual sample rows for them as well.
	targets:=trackerTargetDomains(cfg)
	for host:=range samples {
		if !targets[host] {
			delete(samples,host)
		}
	}

	a.updateTrackerSampleInventory(cfg,manual,autoSamples)
	a.markTrackerSamplesPending(discoveredDomains)
	if len(samples)>0 { a.appendLog("tracker samples ready: %d",len(samples)) }
	return samples
}

func (a *App) runSmartRepair(ctx context.Context,cfg Config) error {
	now:=time.Now()
	a.setJobProgress("读取样本", "读取 Tracker 样本与下载器状态", 1, 7, 0, 0)
	samples:=a.loadSamples(ctx,cfg)
	if err:=ctx.Err(); err!=nil{return fmt.Errorf("repair aborted before mapping changes: %w",err)}
	downloaderFailures:=a.loadDownloaderTrackerFailures(ctx,cfg,samples)
	if err:=ctx.Err(); err!=nil{return fmt.Errorf("repair aborted before mapping changes: %w",err)}
	a.setJobProgress("读取样本", fmt.Sprintf("样本就绪 · %d 个域名配置", len(cfg.Domains)), 1, 7, 1, 1)
	a.mu.RLock()
	current:=copyMappings(a.state.Mappings); health:=copyHealth(a.state.DomainHealth); cachedState:=append([]Candidate(nil),a.state.Candidates...); lastRefresh:=a.state.LastRefresh
	a.mu.RUnlock()
	cached:=freshCandidates(cachedState,time.Duration(cfg.Repair.CandidateTTLMinutes)*time.Minute,now)
	a.appendLog("repair: current=%d cached-candidates=%d",len(current),len(cached))

	mappings:=retainEnabledMappings(current,cfg)
	statuses:=make(map[string]string)
	confirmedUnusableCurrent:=make(map[string]bool)
	pending:=make([]pendingDomain,0)
	freshDownloaderFailure:=make(map[string]bool)
	for index,d:=range cfg.Domains {
		if err:=ctx.Err(); err!=nil{return fmt.Errorf("repair aborted before commit: %w",err)}
		a.setJobDomainProgress("检查当前映射", 2, 7, index+1, len(cfg.Domains), d.Host)
		if !d.Enabled { statuses[d.Host]="disabled"; delete(mappings,d.Host); continue }
		currentIP:=current[d.Host]

		if isFollowDomain(d) {
			delete(mappings,d.Host)
			statuses[d.Host]="follow waiting · "+d.Follow
			continue
		}

		if normalModeSkipsVerification(d) {
			order:=orderedCandidates(cached,d,"","",cfg)
			if len(order)>0 {
				chosen:=order[0]
				mappings[d.Host]=chosen.IP
				statuses[d.Host]=fmt.Sprintf("normal · lowest latency · %s · %.2f ms · verification skipped",chosen.IP,chosen.DelayMS)
				if currentIP!=chosen.IP {
					a.appendLog("%s -> %s (normal strategy · %.2f ms · verification skipped)",d.Host,chosen.IP,chosen.DelayMS)
				}
				continue
			}
			if currentIP!="" {
				mappings[d.Host]=currentIP
				statuses[d.Host]="normal retained · no fresh CFST candidates · verification skipped"
				continue
			}
			statuses[d.Host]="normal waiting for CFST candidates · verification skipped"
			pending=append(pending,pendingDomain{d,"",true})
			continue
		}

		refreshable:=domainRefreshable(d,cfg,samples)
		if !refreshable {
			statuses[d.Host]="tracker sample missing"
			pending=append(pending,pendingDomain{d,currentIP,false})
			continue
		}
		if currentIP!="" {
			if failure,exists:=downloaderFailures[d.Host]; exists && (failure.Hard || downloaderFailureIsNewer(failure,health[d.Host].LastSuccess)) {
				detail:="downloader reported tracker connection failure · "+failure.Detail
				statuses[d.Host]="current failed · "+detail
				a.recordTrackerSampleTest(cfg,d,false,detail)
				freshDownloaderFailure[d.Host]=downloaderFailureIsNewer(failure,health[d.Host].LastFailure)
				skipIP:=currentIP
				if failure.RejectedIP!="" { skipIP=failure.RejectedIP }
				a.appendLog("%s current %s failed from downloader runtime: %s · excluded candidate=%s",d.Host,currentIP,failure.Detail,skipIP)
				pending=append(pending,pendingDomain{d,skipIP,refreshable})
				continue
			}
			domainCtx,cancel,budget,hasBudget:=domainVerificationContext(ctx,len(cfg.Domains)-index)
			if !hasBudget {
				statuses[d.Host]="current verification deferred · insufficient task budget"
				pending=append(pending,pendingDomain{d,currentIP,refreshable})
				continue
			}
			ok,detail:=a.verifyDomain(domainCtx,d,currentIP,cfg,samples)
			domainErr:=domainCtx.Err()
			cancel()
			if domainErr==context.DeadlineExceeded && ctx.Err()==nil {
				detail=fmt.Sprintf("domain verification budget exceeded · %s",budget.Round(time.Second))
			}
			if ok {
				mappings[d.Host]=currentIP; statuses[d.Host]="retained · "+currentIP+" · "+detail
				continue
			}
			if verificationHardFailure(detail) {
				if d.Mode=="http" {
					confirmedUnusableCurrent[d.Host]=true
					delete(mappings,d.Host)
				}
			}
			statuses[d.Host]="current failed · "+detail
			a.appendLog("%s current %s failed: %s",d.Host,currentIP,detail)
		}
		pending=append(pending,pendingDomain{d,currentIP,refreshable})
	}

	// Healthy fast-path: once all configured domains have passed their current
	// mapping/runtime checks, there is nothing to repair. Do not enter cached
	// candidate verification or CFST refresh logic.
	if len(pending)==0 {
		finalNow:=time.Now()
		configured:=make(map[string]bool)
		for _,d:=range cfg.Domains {
			if !d.Enabled { continue }
			configured[d.Host]=true
			if isFollowDomain(d) { continue }
			h:=health[d.Host]
			h.FailureStreak=0
			h.LastSuccess=finalNow.Format(time.RFC3339)
			health[d.Host]=h
		}
		followUnresolved:=applyFollowMappings(cfg,mappings,statuses,health)
		for host:=range health {
			if !configured[host] { delete(health,host) }
		}
		detail:="全部健康"
		if followUnresolved>0 { detail=fmt.Sprintf("%d 个跟随域名未解析",followUnresolved) }
		a.setJobProgress("应用结果", fmt.Sprintf("%s · 提交 %d 个域名映射", detail, len(mappings)), 7, 7, 0, 0)
		if err:=a.commitResolution(ctx,cfg,mappings,statuses,health,"",false);err!=nil{return err}
		a.completeJobProgress("完成", fmt.Sprintf("Repair 检查完成 · %s · %d 个映射", detail, len(mappings)))
		if followUnresolved==0 { a.appendLog("repair: all independent domains healthy; followers synchronized; candidate verification and CFST refresh skipped") }
		return nil
	}

	forceRuntimeRefresh:=false
	for _,p:=range pending {
		if p.Refreshable && freshDownloaderFailure[p.Domain.Host] {
			forceRuntimeRefresh=true
			break
		}
	}
	a.setJobProgress("验证缓存候选", fmt.Sprintf("待处理 %d 个域名", len(pending)), 3, 7, 0, len(pending))
	if forceRuntimeRefresh {
		a.setJobProgress("验证缓存候选", "下载器已确认 Tracker 连接故障，跳过旧候选并优先刷新 CFST", 3, 7, 1, 1)
		a.appendLog("repair: skipping cached candidate verification after fresh downloader Tracker failure; avoiding stale-candidate churn before CFST refresh")
	} else {
		pending=a.resolvePendingWithProgress(ctx,cfg,samples,cached,pending,mappings,statuses,"验证缓存候选",3,7)
	}
	if err:=ctx.Err(); err!=nil{return fmt.Errorf("repair aborted before commit: %w",err)}
	a.setJobProgress("评估刷新", fmt.Sprintf("仍有 %d 个域名待处理", len(pending)), 4, 7, 0, 0)
	maxProspective:=0; refreshablePending:=0
	for _,p:=range pending {
		if !p.Refreshable { continue }
		refreshablePending++; streak:=health[p.Domain.Host].FailureStreak+1; if streak>maxProspective{maxProspective=streak}
	}
	normalNeedsBootstrap:=false
	for _,p:=range pending {
		if p.Domain.Class=="normal" && current[p.Domain.Host]=="" {
			normalNeedsBootstrap=true
			break
		}
	}
	bootstrap:=refreshablePending>0 && len(cached)==0 && (len(current)==0 || normalNeedsBootstrap)
	refreshNow,nextRefresh,backoff:=refreshDecision(now,lastRefresh,maxProspective,cfg.Repair.FailureThreshold,time.Duration(cfg.Repair.RefreshCooldownMinutes)*time.Minute,time.Duration(cfg.Repair.RefreshMaxBackoffMinutes)*time.Minute,bootstrap)
	if forceRuntimeRefresh {
		refreshNow=true
		nextRefresh=time.Time{}
		backoff=0
	}
	var refreshErr error
	a.setJobProgress("评估刷新", fmt.Sprintf("可刷新 %d 个域名", refreshablePending), 4, 7, 1, 1)
	if refreshablePending>0 && refreshNow {
		if forceRuntimeRefresh {
			a.appendLog("repair: starting CFST refresh immediately after downloader tracker connection failure")
		} else {
			a.appendLog("repair: starting CFST refresh (failure streak=%d, backoff=%s)",maxProspective,backoff)
		}
		a.markRefreshAttempt(now); lastRefresh=now.Format(time.RFC3339)
		a.setJobProgress("CFST 测速", "正在刷新候选 IP 池", 5, 7, 0, 0)
		newCandidates,err:=a.runCFST(ctx,cfg)
		if err!=nil {
			refreshErr=err
			a.setJobProgress("CFST 测速", "测速失败 · "+err.Error(), 5, 7, 1, 1)
			a.appendLog("repair: CFST refresh failed: %v",err)
			if forceRuntimeRefresh && len(cached)>0 && ctx.Err()==nil {
				a.setJobProgress("验证新候选", "CFST 未产出新候选，回退验证缓存候选", 6, 7, 0, len(pending))
				pending=a.resolvePendingWithProgress(ctx,cfg,samples,cached,pending,mappings,statuses,"验证新候选",6,7)
				if len(pending)==0 {
					a.appendLog("repair: cached candidate fallback resolved all domains after CFST failure")
					refreshErr=nil
				}
			}
		} else {
			a.storeCandidates(newCandidates)
			a.setJobProgress("CFST 测速", fmt.Sprintf("得到 %d 个新候选", len(newCandidates)), 5, 7, 1, 1)
			a.setJobProgress("验证新候选", fmt.Sprintf("待处理 %d 个域名", len(pending)), 6, 7, 0, len(pending))
			pending=a.resolvePendingWithProgress(ctx,cfg,samples,newCandidates,pending,mappings,statuses,"验证新候选",6,7)
		}
	} else if refreshablePending>0 {
		a.setJobProgress("验证新候选", "无需立即刷新 CFST，保留当前判定", 6, 7, 1, 1)
		if !nextRefresh.IsZero() { a.appendLog("repair: CFST refresh deferred until %s",nextRefresh.Format(time.RFC3339)) } else { a.appendLog("repair: waiting for failure threshold (%d/%d)",maxProspective,cfg.Repair.FailureThreshold) }
	}
	if err:=ctx.Err(); err!=nil{return fmt.Errorf("repair aborted before commit: %w",err)}

	unresolved:=make(map[string]bool,len(pending))
	refreshableUnresolved:=make(map[string]bool)
	for _,p:=range pending {
		unresolved[p.Domain.Host]=true
		if p.Refreshable{refreshableUnresolved[p.Domain.Host]=true}
		finalizeUnresolvedMapping(mappings,current,statuses,p,confirmedUnusableCurrent)
	}

	finalNow:=time.Now(); configured:=make(map[string]bool)
	for _,d:=range cfg.Domains {
		if !d.Enabled { continue }
		configured[d.Host]=true
		if isFollowDomain(d) { continue }
		h:=health[d.Host]
		if unresolved[d.Host] {
			h.FailureStreak++; h.LastFailure=finalNow.Format(time.RFC3339)
			if statuses[d.Host]==""{statuses[d.Host]="no verified candidate"}
		} else {
			h.FailureStreak=0; h.LastSuccess=finalNow.Format(time.RFC3339)
		}
		health[d.Host]=h
	}
	applyFollowMappings(cfg,mappings,statuses,health)
	for host:=range health { if !configured[host]{delete(health,host)} }
	next:=""; maxFinalStreak:=0
	for host:=range refreshableUnresolved { if health[host].FailureStreak>maxFinalStreak{maxFinalStreak=health[host].FailureStreak} }
	if maxFinalStreak>0 {
		_,nextTime,_:=refreshDecision(finalNow,lastRefresh,maxFinalStreak,cfg.Repair.FailureThreshold,time.Duration(cfg.Repair.RefreshCooldownMinutes)*time.Minute,time.Duration(cfg.Repair.RefreshMaxBackoffMinutes)*time.Minute,false)
		if !nextTime.IsZero(){next=nextTime.Format(time.RFC3339)}
	}
	if err:=ctx.Err(); err!=nil{return fmt.Errorf("repair aborted before commit: %w",err)}
	a.setJobProgress("应用结果", fmt.Sprintf("提交 %d 个域名映射", len(mappings)), 7, 7, 0, 0)
	if err:=a.commitResolution(ctx,cfg,mappings,statuses,health,next,false);err!=nil{return err}
	a.reconcileTransmissionTrackerKeepaliveAfterRepair(ctx,cfg,samples,current,mappings)
	if refreshErr!=nil{return refreshErr}
	a.completeJobProgress("完成", fmt.Sprintf("Repair 完成 · %d 个映射", len(mappings)))
	return nil
}

func (a *App) resolvePending(ctx context.Context,cfg Config,samples map[string]TrackerSample,candidates []Candidate,pending []pendingDomain,mappings map[string]string,statuses map[string]string) []pendingDomain {
	return a.resolvePendingWithProgress(ctx,cfg,samples,candidates,pending,mappings,statuses,"验证候选",1,1)
}

func (a *App) resolvePendingWithProgress(ctx context.Context,cfg Config,samples map[string]TrackerSample,candidates []Candidate,pending []pendingDomain,mappings map[string]string,statuses map[string]string,progressStage string,progressStep,progressSteps int) []pendingDomain {
	if len(pending)==0{
		a.setJobProgress(progressStage, "无需处理", progressStep, progressSteps, 1, 1)
		return pending
	}
	remaining:=make([]pendingDomain,0,len(pending))
	for index,p:=range pending {
		if ctx.Err()!=nil {
			remaining=append(remaining,pending[index:]...)
			break
		}
		a.setJobDomainProgress(progressStage, progressStep, progressSteps, index+1, len(pending), p.Domain.Host)
		if !p.Refreshable { remaining=append(remaining,p); continue }
		if normalModeSkipsVerification(p.Domain) {
			order:=orderedCandidates(candidates,p.Domain,"","",cfg)
			if len(order)==0 {
				statuses[p.Domain.Host]="normal unresolved · no CFST candidate"
				remaining=append(remaining,p)
				continue
			}
			chosen:=order[0]
			mappings[p.Domain.Host]=chosen.IP
			statuses[p.Domain.Host]=fmt.Sprintf("normal · lowest latency · %s · %.2f ms · verification skipped",chosen.IP,chosen.DelayMS)
			a.appendLog("%s -> %s (normal strategy · %.2f ms · verification skipped)",p.Domain.Host,chosen.IP,chosen.DelayMS)
			continue
		}

		domainCtx,cancel,budget,hasBudget:=domainVerificationContext(ctx,len(pending)-index)
		if !hasBudget {
			statuses[p.Domain.Host]="no verified candidate · insufficient task budget"
			remaining=append(remaining,p)
			continue
		}
		resolved:=false
		lastDetail:="no candidate"
		order:=orderedCandidates(candidates,p.Domain,"",p.FailedCurrent,cfg)
		for _,candidate:=range order {
			if domainCtx.Err()!=nil {break}
			ok,detail:=a.verifyDomain(domainCtx,p.Domain,candidate.IP,cfg,samples)
			lastDetail=detail
			if !ok { continue }
			mappings[p.Domain.Host]=candidate.IP
			statuses[p.Domain.Host]="verified · "+candidate.IP+" · "+detail
			a.appendLog("%s -> %s (%s)",p.Domain.Host,candidate.IP,detail)
			resolved=true
			break
		}
		domainErr:=domainCtx.Err()
		cancel()
		if resolved {continue}
		if domainErr==context.DeadlineExceeded && ctx.Err()==nil {
			lastDetail=fmt.Sprintf("domain verification budget exceeded · %s",budget.Round(time.Second))
			a.appendLog("%s verification budget exhausted after %s; continuing with next domain",p.Domain.Host,budget.Round(time.Second))
		}
		statuses[p.Domain.Host]="no verified candidate · "+lastDetail
		remaining=append(remaining,p)
	}
	return remaining
}

func finalizeUnresolvedMapping(mappings,current map[string]string,statuses map[string]string,p pendingDomain,confirmedUnusableCurrent map[string]bool){
	oldIP:=current[p.Domain.Host]
	if oldIP=="" {return}
	detail:=statuses[p.Domain.Host]
	if detail=="" {detail="no verified candidate"}
	if confirmedUnusableCurrent[p.Domain.Host] {
		delete(mappings,p.Domain.Host)
		statuses[p.Domain.Host]="unresolved · confirmed HTTP hard failure · old mapping removed · "+oldIP+" · "+detail
		return
	}
	mappings[p.Domain.Host]=oldIP
	statuses[p.Domain.Host]="stale retained · "+oldIP+" · "+detail
}

func refreshDecision(now time.Time,lastRefresh string,failureStreak,threshold int,base,maxBackoff time.Duration,bootstrap bool)(bool,time.Time,time.Duration){
	if bootstrap{return true,time.Time{},0}
	if failureStreak<threshold || failureStreak<=0{return false,time.Time{},0}
	if base<=0{base=time.Hour}; if maxBackoff<base{maxBackoff=base}
	backoff:=base
	for i:=threshold;i<failureStreak && backoff<maxBackoff;i++{backoff*=2;if backoff>maxBackoff{backoff=maxBackoff}}
	last,err:=time.Parse(time.RFC3339,lastRefresh);if err!=nil||last.IsZero(){return true,time.Time{},backoff}
	next:=last.Add(backoff);if !now.Before(next){return true,next,backoff};return false,next,backoff
}

func (a *App) markRefreshAttempt(at time.Time){a.mu.Lock();a.state.LastRefresh=at.Format(time.RFC3339);a.state.NextRefresh="";a.mu.Unlock()}
func (a *App) storeCandidates(candidates []Candidate){a.mu.Lock();a.state.Candidates=rankCandidates(candidates);a.mu.Unlock()}
func copyMappings(in map[string]string)map[string]string{out:=make(map[string]string,len(in));for k,v:=range in{out[k]=v};return out}
func retainEnabledMappings(current map[string]string,cfg Config)map[string]string{
	enabled:=make(map[string]bool,len(cfg.Domains))
	for _,d:=range cfg.Domains{if d.Enabled{enabled[d.Host]=true}}
	out:=make(map[string]string,len(current))
	for host,ip:=range current{if enabled[host]{out[host]=ip}}
	return out
}
func copyHealth(in map[string]DomainHealth)map[string]DomainHealth{out:=make(map[string]DomainHealth,len(in));for k,v:=range in{out[k]=v};return out}
