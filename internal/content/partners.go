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
