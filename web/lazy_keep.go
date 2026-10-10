package web

// R45: functions that stay in their script because the page calls them while
// it starts: a chunk read before the page has drawn costs a round trip.
// Found by the r45 start-up trace (admin, resident and lead; 1440 and 390 px;
// first and repeat visit): every lazy function called in the first 8 s.
// "*:name" matches the name in any script, "<key>:name" one script only
// (key: the script's id, or s<n> for the n-th script without one).
var lazyKeep = map[string]bool{
	"*:addNode": true, "*:anLoadSt": true, "*:api": true, "*:apply": true, "*:applyBarOrder": true,
	"*:applyOrder": true, "*:applyRole": true, "*:applySeed": true, "*:attachBookFiles": true,
	"*:autoCheck": true, "*:badge": true, "*:bar": true, "*:boardsByResident": true, "*:boot": true,
	"*:bsBoot": true, "*:bsNumbers": true, "*:buildSkeleton": true, "*:checkAI": true, "*:checkMeetBanner": true,
	"*:contextHint": true, "*:czDeepLink": true, "*:drawLinks": true, "*:dxHTML": true, "*:ensureBooks": true,
	"*:ensureNetworking": true, "*:firstSync": true, "*:fitAll": true, "*:flush": true, "*:g12Norm": true,
	"*:goHashSection": true, "*:goSection": true, "*:heldDocs": true, "*:helpBtn": true, "*:howHTML": true,
	"*:initReorderBars": true, "*:kb12Norm": true, "*:kb12NormCard": true, "*:leadNorm": true, "*:libHTML": true,
	"*:load": true, "*:loadCRM": true, "*:loadKB": true, "*:loadKanban": true, "*:loadLibs": true,
	"*:loadPL": true, "*:loadQ": true, "*:loadResLib": true, "*:loadUseful": true, "*:lsSet": true,
	"*:markDirty": true, "*:midHTML": true, "*:mktNotesToIdeas": true, "*:newBoard": true, "*:numbersHTML": true,
	"*:paint": true, "*:paintTabs": true, "*:panel": true, "*:pathHTML": true, "*:pfCrumb": true,
	"*:pfSideSync": true, "*:pgSet": true, "*:pointMeta": true, "*:pollCalls": true, "*:pull": true,
	"*:pushAll": true, "*:pushDoc": true, "*:r12Sweep": true, "*:r6Api": true, "*:r8DocMigrate": true,
	"*:recCard": true, "*:recLeftovers": true, "*:redraw": true, "*:renderAbout": true, "*:renderHint": true,
	"*:renderHist": true, "*:renderNode": true, "*:renderSide": true, "*:renderSideAcc": true, "*:scan": true,
	"*:setMode": true, "*:setStatus": true, "*:setupDrag": true, "*:startSync": true, "*:strip": true,
	"*:subDrag": true, "*:tabDrag": true, "*:tick": true, "*:trackData": true, "*:ui": true,
	"*:viewSettle": true, "*:warmTour": true, "*:watch": true,
	// Storage under quota: lsSet calls these at once and uses their answers (a lazy one answered
	// undefined and the write threw); big sections go to IndexedDB before the first sync
	"*:spare": true, "*:toIdb": true, "*:idbOpen": true, "*:idbPut": true, "*:idbFlush": true,
	"*:budget": true, "*:budgetSoon": true, "*:cleanupOnce": true, "*:quotaNote": true,
	// R80: called while the page starts (found by the r45 trace after R69/R72/R79)
	"asScript:applyAssist": true, "r38cJs:refreshBadge": true, "r69Script:buttons": true, "r79Script:mountUI": true,
	"r46Script:r46LeadTeaser": true, // R46: the lead home's «Бизнес-идеи» block
	"*:lxVideo":               true, // R73: the lead home's video starts as the page draws
	// R52: the tab strip keeps its scroll, and the board signs edited cards (who, when) as it draws
	"r52Script:keepStrip": true, "r52Script:edBadge": true, "r52Script:when": true,
	// R52: the https guard in <head> runs before anything is fetched
	"r52Https:up": true, "r52Https:pic": true, "r52Https:set": true, "r52Https:hook": true, "r52Https:attr": true, "r52Https:html": true,
	// R64: a board card is drawn from these on the first paint: a lazy one returned
	// "undefined" in place of the diagnosis or tool cover and the task's deadline line
	"*:coverHTML": true, "*:coverArt": true, "*:coverKey": true, "*:organArt": true, "*:cvTitleFit": true,
	"*:taskMeta": true, "*:goalMeta": true, "*:dnaHelix": true, "*:questBody": true, "*:contBody": true,
	"*:fcAllowed": true, "*:fcOpen": true, "*:money": true,
	// R64: the tour opens by itself on a first visit and from «Обучение» at any moment: its card is built
	// from these, and a lazy one answered undefined (no «Звук» and close buttons, no steps)
	"r16Script:ea": true, "r16Script:mode": true, "r16Script:online": true, "r16Script:dataReady": true,
	"r16Script:obRead": true, "r16Script:obWrite": true, "r16Script:obMark": true, "r16Script:visible": true,
	"r16Script:inSide": true, "r16Script:small": true, "r16Script:navOf": true, "r16Script:tabsOf": true,
	"r16Script:homeBlock": true, "r16Script:buildSteps": true, "r16Script:stepEl": true, "r16Script:sideOpen": true,
	"r16Script:usable": true, "r16Script:root": true, "r16Script:muteBtn": true, "r16Script:showWelcome": true,
	"r16Script:ctaLater": true, "r16Script:ctaOn": true, "r16Script:startSteps": true, "r16Script:go": true,
	"r16Script:render": true, "r16Script:placeCenter": true, "r16Script:position": true, "r16Script:bindCard": true,
	"r16Script:finish": true, "r16Script:requeue": true, "r16Script:start": true,
	// R66: «Решения ИИ» opens straight from a link (#aiRec, the bot's old «Подробнее на платформе»):
	// its page is drawn from these on the first paint; a lazy one answered undefined
	"*:renderAIRec": true, "*:aiwHTML": true, "*:aiwInboxHTML": true, "*:aiwAsk": true, "*:aiwSide": true,
	"*:aiwTargets": true, "*:aiwLib": true, "*:aiwStatusHTML": true, "*:aiwStatus": true, "*:aiwCard": true,
	"*:aiwRunHTML": true, "*:aiwErrHTML": true, "*:bsAiErr": true,
	// R70: the tab rows take their saved order and the partner button is drawn as the board opens
	// (their answers are used at once: a lazy one answered undefined)
	"r70Script:r70scan": true, "r70Script:r70apply": true, "r70Script:r70prtOf": true, "r70Script:chip": true,
	"r70Script:r70boardChip": true, "r70Script:r70rpChip": true,
}

func lazyKeepFn(key, name string) bool { return lazyKeep["*:"+name] || lazyKeep[key+":"+name] }
