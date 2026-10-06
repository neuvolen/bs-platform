package content

import _ "embed"

// PartnersSeed: partners found for «Маркетинг → Партнёрства и UGC» (October
// 2026), merged into the club document bs_partners by key; the team's edits
// and deletions win.
//
//go:embed partners_seed.json
var PartnersSeed []byte

// CrmPipes: the CRM pipelines (воронки) and the default rules that map a
// lead to one by its source: the first pipeline with a matching token wins,
// the last one (no tokens) takes the rest. The platform keeps the same list.
//
//go:embed crm_pipes.json
var CrmPipes []byte

// CrmSegs: R47: the source rules of the CRM segments (crm_segments.go): the
// first rule with a token found in the lead's source column, file name,
// source or utm names the source. The team edits them on the platform
// (bs_crm.segRules); the platform keeps the same list.
//
//go:embed crm_segs.json
var CrmSegs []byte
