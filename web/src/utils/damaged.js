// Shared wording for the catalog's damaged-data warning. The flag is a
// replicated snapshot that can lag by about a minute, so it is a warning,
// not a guarantee -- restore is never blocked on it.
export const DAMAGED_TOOLTIP = 'Backup data for this version is damaged; restore will fail.'
