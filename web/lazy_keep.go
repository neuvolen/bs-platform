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
}

func lazyKeepFn(key, name string) bool { return lazyKeep["*:"+name] || lazyKeep[key+":"+name] }
