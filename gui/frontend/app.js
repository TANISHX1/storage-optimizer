// ==========================================================================
// Intelligent Storage Optimizer — macOS HIG Application Controller
// Communicates with Go Core REST API (http://127.0.0.1:8080/api/v1)
// & Native Wails v2 Runtime Bindings (window.go.main.App)
// ==========================================================================

class StorageApp {
  constructor() {
    this.apiBase = 'http://127.0.0.1:8080/api/v1';
    this.currentTab = 'dashboard';
    this.scanPollTimer = null;
    this.healthPollTimer = null;

    // Cache state
    this.stats = null;
    this.snapshots = [];
    this.duplicates = [];
    this.staleFiles = [];
    this.auditLogs = [];

    // Duplicates Pagination (Fix 5)
    this.dupPage = 1;
    this.dupLimit = 25;
    this.dupTotalPages = 1;
    this.dupTotalGroups = 0;

    // Stale Pagination (Fix 5)
    this.staleDays = 30;
    this.stalePage = 1;
    this.staleLimit = 50;
    this.staleTotalPages = 1;
    this.staleTotalFiles = 0;

    // Directory Browser State (Fix 6)
    this.browseCurrentPath = '';
    this.browseParentPath = '';
    this.browseData = null;

    // Breakdown Analytics State
    this.dupBreakdownMode = 'ext';
    this.dupBreakdownData = null;
    this.staleBreakdownData = null;

    // Modal State
    this.pendingAction = null; // { mode: 'trash'|'permanent', ids: [], files: [] }

    this.init();
  }

  init() {
    this.setupNavigation();
    this.setupWindowControls();
    this.checkApiHealth();
    this.healthPollTimer = setInterval(() => this.checkApiHealth(), 10000);
    this.loadAllData();
    this.setupEventListeners();
  }

  toggleSidebar() {
    const frame = document.querySelector('.window-frame');
    if (frame) {
      frame.classList.toggle('sidebar-collapsed');
    }
  }

  setupWindowControls() {
    // Window Traffic Lights
    document.querySelector('.control-dot.close')?.addEventListener('click', () => {
      if (window.runtime?.Quit) {
        window.runtime.Quit();
      } else {
        this.showToast('Window close triggered (Native Shell)', 'info');
      }
    });

    document.querySelector('.control-dot.minimize')?.addEventListener('click', () => {
      if (window.runtime?.WindowMinimise) {
        window.runtime.WindowMinimise();
      }
    });

    document.querySelector('.control-dot.maximize')?.addEventListener('click', () => {
      if (window.runtime?.WindowToggleMaximise) {
        window.runtime.WindowToggleMaximise();
      }
    });
  }

  setupNavigation() {
    document.querySelectorAll('.nav-item').forEach(button => {
      button.addEventListener('click', () => {
        const tab = button.getAttribute('data-tab');
        this.switchTab(tab);
      });
    });
  }

  setupEventListeners() {
    document.getElementById('btn-refresh-all')?.addEventListener('click', () => {
      this.showToast('Refreshing storage metrics...', 'info');
      this.loadAllData();
    });

    document.getElementById('btn-quick-scan')?.addEventListener('click', () => {
      this.switchTab('scanner');
    });

    // Close modal on Escape & Cmd+K search focus
    window.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && this.pendingAction) {
        this.closeModal();
      }
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        const searchInput = document.getElementById('sidebar-quick-search');
        if (searchInput) {
          searchInput.focus();
          searchInput.select();
        }
      }
    });
  }

  filterTabs(query) {
    const q = (query || '').toLowerCase().trim();
    document.querySelectorAll('.nav-item').forEach(item => {
      const text = item.innerText.toLowerCase();
      if (!q || text.includes(q)) {
        item.style.display = 'flex';
      } else {
        item.style.display = 'none';
      }
    });
  }

  switchTab(tabId) {
    this.currentTab = tabId;

    // Update Sidebar Active state
    document.querySelectorAll('.nav-item').forEach(btn => {
      btn.classList.toggle('active', btn.getAttribute('data-tab') === tabId);
    });

    // Update Content Pane
    document.querySelectorAll('.tab-pane').forEach(pane => {
      pane.classList.toggle('active', pane.id === `tab-${tabId}`);
    });

    // Update Page Header Titles
    const titles = {
      dashboard: { title: 'System Dashboard', sub: 'Real-time local storage health, category classification, and cleanup analytics' },
      scanner: { title: 'Filesystem Walker & Indexer', sub: 'Concurrent POSIX metadata indexing and time-series snapshot tracking' },
      duplicates: { title: 'Duplicate File Hunter', sub: 'Two-pass cryptographic deduplication and space reclamation' },
      stale: { title: 'Stale & Inactive Junk Files', sub: 'Mathematical staleness ranking and system log/cache identification' },
      browse: { title: 'Directory Hierarchy Browser', sub: 'Lazy tree navigation with direct child aggregation and category inspection' },
      forecasting: { title: 'AI Storage Forecasting (ML Layer)', sub: 'Time-series growth trajectory, days-until-full estimation, and smart recommendations' },
      audit: { title: 'FreeDesktop XDG Trash & Audit Log', sub: 'Immutable audit trail of past file cleanups and instant file restoration' }
    };

    const t = titles[tabId] || titles.dashboard;
    const titleEl = document.getElementById('page-title');
    const subEl = document.getElementById('page-subtitle');
    if (titleEl) titleEl.innerText = t.title;
    if (subEl) subEl.innerText = t.sub;

    if (tabId === 'forecasting') {
      this.renderForecastView();
    } else if (tabId === 'browse' && !this.browseData) {
      const scanInput = document.getElementById('scan-path-input')?.value || '/home/blazex/Documents';
      this.loadDirectoryBrowse(scanInput);
    }
  }

  // --- Native Wails Integration ---

  async chooseNativeFolder() {
    try {
      if (window.go && window.go.main && window.go.main.App && window.go.main.App.SelectDirectory) {
        const path = await window.go.main.App.SelectDirectory();
        if (path) {
          this.setScanPath(path);
          this.showToast(`Selected directory: ${path}`, 'info');
        }
      } else {
        // Fallback for browser mode
        const currentVal = document.getElementById('scan-path-input')?.value || '/home/blazex/Documents';
        const newPath = prompt('Enter directory path to index:', currentVal);
        if (newPath) {
          this.setScanPath(newPath);
        }
      }
    } catch (err) {
      console.warn('Folder picker fallback:', err);
    }
  }

  // --- API Client ---

  async apiRequest(endpoint, options = {}) {
    try {
      const resp = await fetch(`${this.apiBase}${endpoint}`, {
        headers: { 'Content-Type': 'application/json', ...options.headers },
        ...options
      });
      if (!resp.ok) {
        const errorText = await resp.text();
        throw new Error(errorText || `HTTP ${resp.status}`);
      }
      return await resp.json();
    } catch (err) {
      console.warn(`[API ERROR] ${endpoint}:`, err);
      throw err;
    }
  }

  async checkApiHealth() {
    const dot = document.getElementById('api-status-dot');
    const title = document.getElementById('api-status-title');
    const sub = document.getElementById('api-status-sub');

    try {
      const data = await this.apiRequest('/health');
      if (data && data.status === 'healthy') {
        if (dot) dot.className = 'status-indicator-dot online';
        if (title) title.innerText = 'Go Core Active';
        if (sub) sub.innerText = '127.0.0.1:8080';
      }
    } catch (err) {
      if (dot) dot.className = 'status-indicator-dot';
      if (title) title.innerText = 'Core Disconnected';
      if (sub) sub.innerText = 'Start ./bin/storage-optimizer serve';
    }
  }

  // --- Data Loading ---

  async loadAllData() {
    try {
      await Promise.allSettled([
        this.loadStats(),
        this.loadDuplicates(),
        this.loadStaleFiles(30),
        this.loadSnapshots(),
        this.loadAuditLogs()
      ]);
      if (this.stats) {
        this.renderDashboardStats(this.stats);
        this.renderStorageHeroBar(this.stats);
      }
    } catch (e) {
      console.error('Error during data load:', e);
    }
  }

  async loadStats() {
    try {
      const stats = await this.apiRequest('/stats');
      this.stats = stats;
      this.renderDashboardStats(stats);
      this.renderStorageHeroBar(stats);
      this.renderCategoryChart(stats.categories, stats.total_bytes);
    } catch (err) {
      console.error('Failed to load stats:', err);
    }
  }

  async loadDuplicates(page = 1) {
    try {
      this.dupPage = page;
      const data = await this.apiRequest(`/files/duplicates?page=${page}&limit=${this.dupLimit}`);
      this.duplicates = data.groups || [];
      this.dupTotalPages = data.total_pages || 1;
      this.dupTotalGroups = data.total_groups || 0;

      const badge = document.getElementById('badge-duplicates');
      if (badge) badge.innerText = this.dupTotalGroups;

      const summaryBadge = document.getElementById('dup-summary-badge');
      if (summaryBadge) summaryBadge.innerText = `${this.dupTotalGroups} Groups (${this.formatBytes(data.total_wasted_bytes || 0)} wasted)`;

      this.renderDuplicatesList();
      this.renderDupPagination(data);
      if (page === 1 && !this.dupBreakdownData) {
        setTimeout(() => this.loadDuplicatesBreakdown(), 0);
      }
    } catch (err) {
      console.error('Failed to load duplicates:', err);
    }
  }

  async loadDuplicatesBreakdown() {
    try {
      const data = await this.apiRequest('/files/duplicates/breakdown');
      this.dupBreakdownData = data;
      this.renderDuplicatesBreakdown();
    } catch (err) {
      console.error('Failed to load duplicates breakdown:', err);
    }
  }

  renderDuplicatesBreakdown() {
    const body = document.getElementById('dup-breakdown-body');
    if (!body) return;

    const exts = (this.dupBreakdownData && this.dupBreakdownData.extensions) || [];
    if (exts.length === 0) {
      body.innerHTML = `<div style="width: 100%; padding: 14px; text-align: center; color: var(--text-tertiary); font-size: 0.8rem;">No duplicate file extensions detected. Run a scan to discover duplicate file types.</div>`;
      return;
    }

    const C = 219.91; // 2 * PI * 35
    let html = '';
    exts.forEach(item => {
      const pctVal = Math.min(100, Math.max(0, item.percentage || 0));
      const pctStr = pctVal.toFixed(1);
      const dashoffset = (C * (1 - pctVal / 100)).toFixed(2);
      let extLabel = (item.extension || 'other').toUpperCase();
      if (extLabel.startsWith('.')) extLabel = extLabel.substring(1);
      if (!extLabel) extLabel = 'FILE';

      html += `
        <div class="circle-node-wrapper">
          <div class="circle-node">
            <svg viewBox="0 0 80 80">
              <circle class="ring-bg" cx="40" cy="40" r="35"></circle>
              <circle class="ring-fill ring-purple" cx="40" cy="40" r="35"
                style="stroke-dasharray: 219.91; stroke-dashoffset: ${dashoffset};"></circle>
            </svg>
            <div class="circle-label-wrap">
              <span class="circle-ext-text">${extLabel}</span>
              <span class="circle-pct-text">${pctStr}%</span>
            </div>
            <div class="tooltip-box">
              <div class="tooltip-title">
                <span class="badge badge-purple">.${extLabel.toLowerCase()}</span>
                <span>${item.count.toLocaleString()} Duplicates</span>
              </div>
              <div class="tooltip-row">Wasted Space: <strong>${this.formatBytes(item.total_bytes)}</strong></div>
              <div class="tooltip-row">Share of Duplicates: <strong>${pctStr}%</strong></div>
            </div>
          </div>
        </div>
      `;
    });
    body.innerHTML = html;
  }

  prevDupPage() {
    if (this.dupPage > 1) {
      this.loadDuplicates(this.dupPage - 1);
    }
  }

  nextDupPage() {
    if (this.dupPage < this.dupTotalPages) {
      this.loadDuplicates(this.dupPage + 1);
    }
  }

  renderDupPagination(data) {
    const bar = document.getElementById('dup-pagination-bar');
    if (!bar) return;

    if (this.dupTotalGroups === 0) {
      bar.style.display = 'none';
      return;
    }

    bar.style.display = 'flex';
    const info = document.getElementById('dup-pagination-info');
    if (info) info.innerText = `Showing page ${this.dupPage} of ${this.dupTotalPages} (${this.dupTotalGroups} groups, ${this.formatBytes(data.total_wasted_bytes || 0)} wasted)`;

    const ind = document.getElementById('dup-page-indicator');
    if (ind) ind.innerText = `${this.dupPage} / ${this.dupTotalPages}`;

    const prevBtn = document.getElementById('btn-dup-prev');
    const nextBtn = document.getElementById('btn-dup-next');
    if (prevBtn) prevBtn.disabled = this.dupPage <= 1;
    if (nextBtn) nextBtn.disabled = this.dupPage >= this.dupTotalPages;
  }

  async loadStaleFiles(days = 30, page = 1) {
    try {
      this.staleDays = days;
      this.stalePage = page;

      // Update segmented control buttons
      document.querySelectorAll('#stale-segmented-control .segment-btn').forEach(b => {
        b.classList.toggle('active', parseInt(b.getAttribute('data-days')) === days);
      });

      const data = await this.apiRequest(`/files/stale?days=${days}&min_score=0.01&page=${page}&limit=${this.staleLimit}`);
      this.staleFiles = data.files || [];
      this.staleTotalPages = data.total_pages || 1;
      this.staleTotalFiles = data.total_files || 0;

      const badge = document.getElementById('badge-stale');
      if (badge) badge.innerText = this.staleTotalFiles;

      this.renderStaleTable();
      this.renderStalePagination(data);
      if (page === 1 && !this.staleBreakdownData) {
        setTimeout(() => this.loadStaleBreakdown(), 0);
      }
      if (this.stats) {
        this.renderDashboardStats(this.stats);
        this.renderStorageHeroBar(this.stats);
      }
    } catch (err) {
      console.error('Failed to load stale files:', err);
    }
  }

  async loadStaleBreakdown() {
    try {
      const data = await this.apiRequest('/files/stale/breakdown');
      this.staleBreakdownData = data;
      this.renderStaleBreakdown();
    } catch (err) {
      console.error('Failed to load stale breakdown:', err);
    }
  }

  renderStaleBreakdown() {
    const body = document.getElementById('stale-breakdown-body');
    if (!body) return;

    const exts = (this.staleBreakdownData && this.staleBreakdownData.extensions) || [];
    if (exts.length === 0) {
      body.innerHTML = `<div style="width: 100%; padding: 14px; text-align: center; color: var(--text-tertiary); font-size: 0.8rem;">No stale file extensions detected. Run a scan to discover inactive files.</div>`;
      return;
    }

    const C = 219.91; // 2 * PI * 35
    let html = '';
    exts.forEach(item => {
      const pctVal = Math.min(100, Math.max(0, item.percentage || 0));
      const pctStr = pctVal.toFixed(1);
      const dashoffset = (C * (1 - pctVal / 100)).toFixed(2);
      let extLabel = (item.extension || 'other').toUpperCase();
      if (extLabel.startsWith('.')) extLabel = extLabel.substring(1);
      if (!extLabel) extLabel = 'FILE';

      html += `
        <div class="circle-node-wrapper">
          <div class="circle-node">
            <svg viewBox="0 0 80 80">
              <circle class="ring-bg" cx="40" cy="40" r="35"></circle>
              <circle class="ring-fill ring-amber" cx="40" cy="40" r="35"
                style="stroke-dasharray: 219.91; stroke-dashoffset: ${dashoffset};"></circle>
            </svg>
            <div class="circle-label-wrap">
              <span class="circle-ext-text">${extLabel}</span>
              <span class="circle-pct-text">${pctStr}%</span>
            </div>
            <div class="tooltip-box">
              <div class="tooltip-title">
                <span class="badge badge-amber">.${extLabel.toLowerCase()}</span>
                <span>${item.count.toLocaleString()} Stale Files</span>
              </div>
              <div class="tooltip-row">Stale Storage: <strong>${this.formatBytes(item.total_bytes)}</strong></div>
              <div class="tooltip-row">Share of Inactive: <strong>${pctStr}%</strong></div>
            </div>
          </div>
        </div>
      `;
    });
    body.innerHTML = html;
  }

  prevStalePage() {
    if (this.stalePage > 1) {
      this.loadStaleFiles(this.staleDays, this.stalePage - 1);
    }
  }

  nextStalePage() {
    if (this.stalePage < this.staleTotalPages) {
      this.loadStaleFiles(this.staleDays, this.stalePage + 1);
    }
  }

  renderStalePagination(data) {
    const bar = document.getElementById('stale-pagination-bar');
    if (!bar) return;

    if (this.staleTotalFiles === 0) {
      bar.style.display = 'none';
      return;
    }

    bar.style.display = 'flex';
    const info = document.getElementById('stale-pagination-info');
    if (info) info.innerText = `Showing page ${this.stalePage} of ${this.staleTotalPages} (${this.staleTotalFiles} stale files, ${this.formatBytes(data.total_bytes || 0)})`;

    const ind = document.getElementById('stale-page-indicator');
    if (ind) ind.innerText = `${this.stalePage} / ${this.staleTotalPages}`;

    const prevBtn = document.getElementById('btn-stale-prev');
    const nextBtn = document.getElementById('btn-stale-next');
    if (prevBtn) prevBtn.disabled = this.stalePage <= 1;
    if (nextBtn) nextBtn.disabled = this.stalePage >= this.staleTotalPages;
  }

  // --- Directory Hierarchy Browser (Fix 6) ---

  async loadDirectoryBrowse(path = '/') {
    try {
      const data = await this.apiRequest(`/files/browse?path=${encodeURIComponent(path)}`);
      this.browseData = data;
      this.browseCurrentPath = data.current_path || path;
      this.browseParentPath = data.parent_path || '';
      this.renderBrowseView();
    } catch (err) {
      console.error('Failed to load directory browse:', err);
      this.showToast(`Directory lookup error: ${err.message}`, 'error');
    }
  }

  browsePath(path) {
    this.loadDirectoryBrowse(path);
  }

  browseUpDirectory() {
    if (this.browseParentPath && this.browseParentPath !== this.browseCurrentPath) {
      this.browsePath(this.browseParentPath);
    }
  }

  renderBrowseView() {
    if (!this.browseData) return;

    // Breadcrumbs
    const crumbsContainer = document.getElementById('browse-breadcrumbs');
    if (crumbsContainer) {
      const parts = this.browseCurrentPath.split('/').filter(Boolean);
      let accumulated = '';
      let crumbsHtml = `<span class="breadcrumb-crumb" onclick="app.browsePath('/')">root</span>`;

      parts.forEach((p) => {
        accumulated += '/' + p;
        const clickPath = accumulated;
        crumbsHtml += ` <span style="color: var(--text-tertiary);">/</span> <span class="breadcrumb-crumb" onclick="app.browsePath('${clickPath}')">${p}</span>`;
      });
      crumbsContainer.innerHTML = crumbsHtml;
    }

    const upBtn = document.getElementById('btn-browse-up');
    if (upBtn) {
      upBtn.disabled = !this.browseParentPath || this.browseCurrentPath === '/' || this.browseParentPath === this.browseCurrentPath;
    }

    // Table
    const tbody = document.getElementById('browse-table-body');
    if (!tbody) return;

    const items = this.browseData.items || [];
    const dirs = items.filter(it => it.is_dir);
    const files = items.filter(it => !it.is_dir);

    if (dirs.length === 0 && files.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="empty-cell">Directory is empty.</td></tr>';
      return;
    }

    let rowsHtml = '';

    // Subdirectories first (scanned vs unscanned)
    dirs.forEach(d => {
      const isScanned = d.is_scanned !== false;
      if (isScanned) {
        rowsHtml += `
          <tr class="dir-item-row" onclick="app.browsePath('${d.path.replace(/'/g, "\\'")}')">
            <td>
              <span class="dir-icon">📁</span>
              <strong>${d.name}</strong>
            </td>
            <td><span class="badge badge-blue">Indexed</span></td>
            <td class="size-cell">${this.formatBytes(d.size)}</td>
            <td><span style="color: var(--text-tertiary);">${(d.item_count || 0).toLocaleString('en-US')} files</span></td>
            <td style="color: var(--text-secondary); font-size: 0.76rem;">${this.formatTimestamp(d.mtime)}</td>
          </tr>
        `;
      } else {
        rowsHtml += `
          <tr class="dir-item-row dir-unscanned" onclick="app.browsePath('${d.path.replace(/'/g, "\\'")}')" title="Not Scanned — Click to browse physical folder">
            <td style="opacity: 0.5;">
              <span class="dir-icon" style="filter: grayscale(1); opacity: 0.6;">📁</span>
              <span style="color: var(--text-tertiary); font-weight: 500;">${d.name}</span>
            </td>
            <td><span class="badge badge-unscanned">Not Indexed</span></td>
            <td class="size-cell" style="opacity: 0.35;">--</td>
            <td style="opacity: 0.35;"><span style="color: var(--text-tertiary);">0 files</span></td>
            <td style="color: var(--text-tertiary); font-size: 0.76rem; opacity: 0.35;">${this.formatTimestamp(d.mtime)}</td>
          </tr>
        `;
      }
    });

    // Direct files
    files.forEach(f => {
      rowsHtml += `
        <tr>
          <td>
            <span class="dir-icon">📄</span>
            <span>${f.name}</span>
          </td>
          <td><span class="badge badge-secondary">File</span></td>
          <td class="size-cell">${this.formatBytes(f.size)}</td>
          <td>${this.renderCategoryBadge(f.category)}</td>
          <td style="color: var(--text-secondary); font-size: 0.76rem;">${this.formatTimestamp(f.mtime)}</td>
        </tr>
      `;
    });

    tbody.innerHTML = rowsHtml;
  }

  async loadSnapshots() {
    try {
      const data = await this.apiRequest('/snapshots?limit=50');
      this.snapshots = data.snapshots || [];
      const badge = document.getElementById('snapshots-count-badge');
      if (badge) badge.innerText = `${this.snapshots.length} Records`;
      this.renderSnapshotsTable();
      if (this.currentTab === 'forecasting') {
        this.renderForecastView();
      }
    } catch (err) {
      console.error('Failed to load snapshots:', err);
    }
  }

  async loadAuditLogs() {
    try {
      const data = await this.apiRequest('/actions/history?limit=100');
      this.auditLogs = data.actions || [];
      const badge = document.getElementById('audit-count-badge');
      const sideBadge = document.getElementById('badge-audit');
      if (badge) badge.innerText = `${this.auditLogs.length} Records`;
      if (sideBadge) sideBadge.innerText = this.auditLogs.length;
      this.renderAuditTable();
    } catch (err) {
      console.error('Failed to load audit logs:', err);
    }
  }

  // --- Rendering Functions ---

  renderDashboardStats(stats) {
    document.getElementById('stat-total-storage').innerText = this.formatBytes(stats.total_bytes);
    document.getElementById('stat-total-files').innerText = `${(stats.total_files || 0).toLocaleString('en-US')} files indexed`;

    document.getElementById('stat-duplicate-bytes').innerText = this.formatBytes(stats.total_wasted_bytes);
    const dupCount = stats.total_duplicate_files || stats.total_duplicates || 0;
    const dupGroups = stats.total_duplicates || 0;
    document.getElementById('stat-duplicate-count').innerText = `${dupCount.toLocaleString('en-US')} redundant files (${dupGroups.toLocaleString('en-US')} groups)`;

    const staleBytes = stats.total_stale_bytes !== undefined && stats.total_stale_bytes > 0 ? stats.total_stale_bytes : (this.staleTotalBytes || 0);
    const staleCount = stats.total_stale_files !== undefined && stats.total_stale_files > 0 ? stats.total_stale_files : (this.staleTotalFiles || 0);
    document.getElementById('stat-stale-bytes').innerText = this.formatBytes(staleBytes);
    document.getElementById('stat-stale-count').innerText = `${staleCount.toLocaleString('en-US')} files (30+ days)`;

    const reclaimable = (stats.total_wasted_bytes || 0) + staleBytes;
    document.getElementById('stat-reclaimable-total').innerText = this.formatBytes(reclaimable);
  }

  renderStorageHeroBar(stats) {
    const heroTitle = document.getElementById('storage-hero-title');
    const heroReclaim = document.getElementById('storage-hero-reclaim');
    const bar = document.getElementById('storage-multi-bar');

    if (heroTitle) heroTitle.innerText = `${this.formatBytes(stats.total_bytes)} Indexed Storage`;

    const staleBytes = stats.total_stale_bytes !== undefined && stats.total_stale_bytes > 0 ? stats.total_stale_bytes : (this.staleTotalBytes || 0);
    const reclaimable = (stats.total_wasted_bytes || 0) + staleBytes;
    if (heroReclaim) heroReclaim.innerText = `${this.formatBytes(reclaimable)} Safe Cleanup Potential`;

    if (!bar || !stats.categories) return;

    let userBytes = 0;
    let sysBytes = 0;
    let cacheBytes = 0;
    const dupBytes = stats.total_wasted_bytes || 0;

    stats.categories.forEach(c => {
      if (c.category === 'user') userBytes += c.total_bytes;
      else if (c.category === 'system_protected') sysBytes += c.total_bytes;
      else cacheBytes += c.total_bytes;
    });

    const uniqueUserBytes = Math.max(0, userBytes - dupBytes);
    const total = stats.total_bytes || 1;
    const userPct = Math.max(Math.round((uniqueUserBytes / total) * 100), uniqueUserBytes > 0 ? 1 : 0);
    const sysPct = Math.max(Math.round((sysBytes / total) * 100), sysBytes > 0 ? 1 : 0);
    const cachePct = Math.max(Math.round((cacheBytes / total) * 100), cacheBytes > 0 ? 1 : 0);
    const dupPct = Math.max(Math.round((dupBytes / total) * 100), dupBytes > 0 ? 1 : 0);

    bar.innerHTML = `
      <div class="bar-segment user" style="width: ${userPct}%" title="User Files: ${this.formatBytes(uniqueUserBytes)}"></div>
      <div class="bar-segment cache" style="width: ${cachePct}%" title="Logs & Cache: ${this.formatBytes(cacheBytes)}"></div>
      <div class="bar-segment dup" style="width: ${dupPct}%" title="Duplicates: ${this.formatBytes(dupBytes)}"></div>
      <div class="bar-segment sys" style="width: ${sysPct}%" title="System Protected: ${this.formatBytes(sysBytes)}"></div>
    `;

    document.getElementById('bar-val-user').innerText = this.formatBytes(uniqueUserBytes);
    document.getElementById('bar-val-sys').innerText = this.formatBytes(sysBytes);
    document.getElementById('bar-val-cache').innerText = this.formatBytes(cacheBytes);
    document.getElementById('bar-val-dup').innerText = this.formatBytes(dupBytes);
  }

  renderCategoryChart(categories = [], totalBytes = 0) {
    const legend = document.getElementById('category-legend-list');
    const segmentsContainer = document.getElementById('donut-segments');
    const centerVal = document.getElementById('donut-center-val');

    if (!legend || !segmentsContainer) return;

    centerVal.textContent = this.formatBytes(totalBytes);
    legend.innerHTML = '';
    segmentsContainer.innerHTML = '';

    const colors = {
      user: '#ff453a',              // systemRed (dark)
      system_protected: '#98989d',  // systemGray (dark)
      system_log: '#ff9f0a',        // systemOrange (dark)
      temp: '#ff9f0a',              // systemOrange (dark)
      crash_dump: '#ff453a',        // systemRed (dark)
      system_cache: '#ff9f0a'       // systemOrange (dark)
    };

    const circumference = 2 * Math.PI * 38; // r=38 -> ~238.76
    let accumulatedOffset = 0;

    categories.forEach(cat => {
      const fraction = totalBytes > 0 ? cat.total_bytes / totalBytes : 0;
      const strokeLength = fraction * circumference;
      const strokeSpace = circumference - strokeLength;
      const color = colors[cat.category] || '#8e8e93';

      // SVG Donut segment
      if (fraction > 0.001) {
        const circle = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
        circle.setAttribute('class', 'donut-segment');
        circle.setAttribute('cx', '50');
        circle.setAttribute('cy', '50');
        circle.setAttribute('r', '38');
        circle.setAttribute('fill', 'transparent');
        circle.setAttribute('stroke', color);
        circle.setAttribute('stroke-width', '11');
        circle.setAttribute('stroke-dasharray', `${strokeLength} ${strokeSpace}`);
        circle.setAttribute('stroke-dashoffset', `${-accumulatedOffset}`);
        circle.setAttribute('stroke-linecap', 'round');
        segmentsContainer.appendChild(circle);

        accumulatedOffset += strokeLength;
      }

      // Legend Item
      const item = document.createElement('div');
      item.className = 'legend-item';
      item.innerHTML = `
        <div class="legend-color-label">
          <span class="legend-dot" style="background: ${color}"></span>
          <span>${this.formatCategoryName(cat.category)}</span>
        </div>
        <span class="legend-value">${this.formatBytes(cat.total_bytes)} (${(cat.total_files || 0).toLocaleString('en-US')})</span>
      `;
      legend.appendChild(item);
    });
  }

  renderDuplicatesList() {
    const container = document.getElementById('duplicates-container');
    if (!container) return;

    if (!this.duplicates || this.duplicates.length === 0) {
      container.innerHTML = `
        <div class="empty-state">
          <div class="empty-icon-wrap">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
          </div>
          <h3>Zero Duplicates Found</h3>
          <p>Your storage index is clean and free of redundant identical files.</p>
        </div>
      `;
      return;
    }

    container.innerHTML = this.duplicates.map((group) => {
      const wasted = group.wasted_bytes || 0;
      const hashStr = group.content_hash || group.hash || '';
      return `
        <div class="duplicate-cluster-card">
          <div class="cluster-header">
            <div class="cluster-hash">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>
              <span>SHA-256: ${hashStr ? hashStr.substring(0, 16) + '...' : 'Unknown'}</span>
            </div>
            <div class="cluster-meta">
              <span>${group.files.length} Copies</span> • <span class="text-amber">${this.formatBytes(wasted)} Reclaimable</span>
            </div>
          </div>
          <div class="cluster-files-list">
            ${group.files.map((file, fileIdx) => {
              const isFirst = fileIdx === 0;
              return `
                <div class="duplicate-item-row ${isFirst ? 'is-original' : ''}">
                  <input type="checkbox" class="dup-checkbox" data-id="${file.id}" data-path="${file.path}" data-size="${file.size}" ${!isFirst ? 'checked' : ''} onchange="app.updateDuplicateActionButtons()">
                  <span class="path-cell" title="${file.path}">${file.path}</span>
                  <span class="size-cell">${this.formatBytes(file.size)}</span>
                  ${isFirst ? '<span class="badge badge-green">Original (Keep)</span>' : '<span class="badge badge-amber">Duplicate Copy</span>'}
                </div>
              `;
            }).join('')}
          </div>
        </div>
      `;
    }).join('');

    this.updateDuplicateActionButtons();
  }

  updateDuplicateActionButtons() {
    const checked = document.querySelectorAll('.dup-checkbox:checked');
    const btnTrash = document.getElementById('btn-trash-duplicates');
    const btnPerm = document.getElementById('btn-perm-duplicates');
    const hasSelection = checked.length > 0;

    if (btnTrash) btnTrash.disabled = !hasSelection;
    if (btnPerm) btnPerm.disabled = !hasSelection;
  }

  selectDuplicateCopies() {
    document.querySelectorAll('.dup-checkbox').forEach((cb) => {
      const isOriginal = cb.closest('.duplicate-item-row').classList.contains('is-original');
      cb.checked = !isOriginal;
    });
    this.updateDuplicateActionButtons();
    this.showToast('Selected all redundant duplicate copies.', 'info');
  }

  renderStaleTable() {
    const tbody = document.getElementById('stale-table-body');
    if (!tbody) return;

    if (!this.staleFiles || this.staleFiles.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty-cell">No stale or inactive files found for this age threshold.</td></tr>';
      return;
    }

    tbody.innerHTML = this.staleFiles.map(file => {
      const scorePct = Math.round((file.staleness_score || 0) * 100);
      const scoreColor = scorePct > 80 ? 'var(--apple-red)' : scorePct > 50 ? 'var(--apple-orange)' : 'var(--apple-blue)';
      const isProtected = file.category === 'system_protected';

      return `
        <tr>
          <td><input type="checkbox" class="stale-checkbox" data-id="${file.id}" data-path="${file.path}" data-size="${file.size}" data-cat="${file.category}" ${isProtected ? 'disabled' : ''} onchange="app.updateStaleActionButtons()"></td>
          <td>
            <div class="staleness-meter"><div class="staleness-fill" style="width: ${scorePct}%; background: ${scoreColor}"></div></div>
            <span style="font-family: var(--font-mono); font-size: 0.78rem">${(file.staleness_score || 0).toFixed(2)}</span>
          </td>
          <td>${this.renderCategoryBadge(file.category)}</td>
          <td class="path-cell" title="${file.path}">${file.path}</td>
          <td class="size-cell">${this.formatBytes(file.size)}</td>
          <td style="color: var(--text-secondary); font-size: 0.76rem;">${this.formatTimestamp(file.atime || file.mtime)}</td>
        </tr>
      `;
    }).join('');

    this.updateStaleActionButtons();
  }

  toggleAllStaleCheckboxes(checked) {
    document.querySelectorAll('.stale-checkbox:not(:disabled)').forEach(cb => {
      cb.checked = checked;
    });
    this.updateStaleActionButtons();
  }

  selectAllStale() {
    document.querySelectorAll('.stale-checkbox:not(:disabled)').forEach(cb => {
      cb.checked = true;
    });
    const checkAll = document.getElementById('check-all-stale');
    if (checkAll) checkAll.checked = true;
    this.updateStaleActionButtons();
    this.showToast('Selected safe candidate files for cleanup.', 'info');
  }

  updateStaleActionButtons() {
    const checked = document.querySelectorAll('.stale-checkbox:checked');
    const btnTrash = document.getElementById('btn-trash-stale');
    const btnPerm = document.getElementById('btn-perm-stale');
    const hasSelection = checked.length > 0;

    if (btnTrash) btnTrash.disabled = !hasSelection;
    if (btnPerm) btnPerm.disabled = !hasSelection;
  }

  renderSnapshotsTable() {
    const tbody = document.getElementById('snapshots-table-body');
    if (!tbody) return;

    if (!this.snapshots || this.snapshots.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="empty-cell">No scan snapshots recorded yet.</td></tr>';
      return;
    }

    tbody.innerHTML = this.snapshots.map(s => `
      <tr>
        <td><span class="badge badge-secondary">#${s.id}</span></td>
        <td>${this.formatTimestamp(s.scanned_at)}</td>
        <td class="path-cell" title="${s.root_path}">${s.root_path}</td>
        <td>${(s.total_files || 0).toLocaleString('en-US')} files</td>
        <td class="size-cell">${this.formatBytes(s.total_bytes)}</td>
      </tr>
    `).join('');
  }

  renderAuditTable() {
    const tbody = document.getElementById('audit-table-body');
    if (!tbody) return;

    if (!this.auditLogs || this.auditLogs.length === 0) {
      tbody.innerHTML = '<tr><td colspan="7" class="empty-cell">No file cleanup actions recorded in audit log.</td></tr>';
      return;
    }

    tbody.innerHTML = this.auditLogs.map(log => {
      const isTrash = log.action_mode === 'trash';
      return `
        <tr>
          <td><span class="badge badge-secondary">#${log.id}</span></td>
          <td><span class="badge ${isTrash ? 'badge-amber' : 'badge-rose'}">${log.action_mode.toUpperCase()}</span></td>
          <td class="path-cell" title="${log.file_path}">${log.file_path}</td>
          <td class="path-cell" style="color: var(--text-tertiary);" title="${log.trashed_to_path || ''}">${log.trashed_to_path || 'None (Removed)'}</td>
          <td class="size-cell">${this.formatBytes(log.file_size)}</td>
          <td style="color: var(--text-secondary); font-size: 0.76rem;">${this.formatTimestamp(log.performed_at)}</td>
          <td>
            ${isTrash ? `<button class="btn btn-sm btn-primary" onclick="app.restoreAction(${log.id})">Restore</button>` : '<span style="color: var(--text-tertiary); font-size: 0.72rem;">Destroyed</span>'}
          </td>
        </tr>
      `;
    }).join('');
  }

  // --- AI Forecasting & ML Canvas (Retina Crisp Scaling) ---

  renderForecastView() {
    const canvas = document.getElementById('forecast-canvas');
    if (!canvas) return;

    const dpr = window.devicePixelRatio || 1;
    const rect = canvas.getBoundingClientRect();
    const cssWidth = rect.width || 900;
    const cssHeight = 300;

    canvas.width = cssWidth * dpr;
    canvas.height = cssHeight * dpr;

    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, cssWidth, cssHeight);

    if (this.snapshots.length < 2) {
      this.drawEmptyForecast(ctx, cssWidth, cssHeight);
      return;
    }

    const parseTimeSec = (val) => {
      if (typeof val === 'number') return val > 1e11 ? val / 1000 : val;
      const d = new Date(val);
      return isNaN(d.getTime()) ? 0 : d.getTime() / 1000;
    };

    const sorted = [...this.snapshots].sort((a, b) => parseTimeSec(a.scanned_at) - parseTimeSec(b.scanned_at));
    const times = sorted.map(s => parseTimeSec(s.scanned_at));
    const bytes = sorted.map(s => s.total_bytes);

    const n = sorted.length;
    const minT = times[0];
    const maxT = times[n - 1];

    let sumX = 0, sumY = 0, sumXY = 0, sumXX = 0;
    for (let i = 0; i < n; i++) {
      const x = (times[i] - minT) / 86400; // days
      const y = bytes[i];
      sumX += x;
      sumY += y;
      sumXY += x * y;
      sumXX += x * x;
    }

    const denominator = n * sumXX - sumX * sumX;
    const slope = denominator !== 0 ? (n * sumXY - sumX * sumY) / denominator : 0;
    const intercept = (sumY - slope * sumX) / n;

    const dailyGrowth = Math.max(slope, 1024 * 1024);
    document.getElementById('forecast-daily-growth').innerText = `${this.formatBytes(dailyGrowth)} / day`;

    const currentBytes = bytes[bytes.length - 1];
    const assumedCapacity = Math.max(currentBytes * 1.4, 120 * 1024 * 1024 * 1024);
    const remainingBytes = Math.max(0, assumedCapacity - currentBytes);
    const daysUntilFull = Math.max(Math.round(remainingBytes / dailyGrowth), 14);

    document.getElementById('forecast-days-until-full').innerText = `${daysUntilFull} Days`;

    this.drawForecastChart(ctx, cssWidth, cssHeight, sorted, slope, intercept, minT, assumedCapacity);
    this.renderAiRecommendations(dailyGrowth, daysUntilFull);
  }

  drawEmptyForecast(ctx, width, height) {
    ctx.fillStyle = 'rgba(255, 255, 255, 0.02)';
    ctx.fillRect(0, 0, width, height);

    ctx.fillStyle = 'rgba(235, 235, 245, 0.45)';
    ctx.font = '500 13px Inter, sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('Perform scans across multiple directories to build time-series snapshots for AI growth modeling.', width / 2, height / 2);
  }

  drawForecastChart(ctx, width, height, snapshots, slope, intercept, minT, assumedCapacity) {
    const padX = 64;
    const padY = 36;
    const plotW = width - padX * 2;
    const plotH = height - padY * 2;

    const maxBytes = Math.max(...snapshots.map(s => s.total_bytes)) * 1.25;
    const minBytes = Math.min(...snapshots.map(s => s.total_bytes)) * 0.85;

    // Grid lines
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.06)';
    ctx.lineWidth = 1;
    for (let i = 0; i <= 4; i++) {
      const y = padY + (plotH / 4) * i;
      ctx.beginPath();
      ctx.moveTo(padX, y);
      ctx.lineTo(width - padX, y);
      ctx.stroke();

      const labelVal = maxBytes - (i / 4) * (maxBytes - minBytes);
      ctx.fillStyle = 'rgba(235, 235, 245, 0.45)';
      ctx.font = '10px JetBrains Mono';
      ctx.textAlign = 'right';
      ctx.fillText(this.formatBytes(labelVal), padX - 10, y + 3);
    }

    // Historical Points Path
    const points = snapshots.map((s, i) => {
      const xRatio = snapshots.length > 1 ? i / (snapshots.length - 1) : 0;
      const x = padX + (plotW * 0.58) * xRatio;
      const yRatio = (s.total_bytes - minBytes) / (maxBytes - minBytes || 1);
      const y = height - padY - (plotH * yRatio);
      return { x, y, bytes: s.total_bytes, time: s.scanned_at };
    });

    // Area Fill Gradient below Historical Curve
    const grad = ctx.createLinearGradient(0, padY, 0, height - padY);
    grad.addColorStop(0, 'rgba(100, 210, 255, 0.20)');
    grad.addColorStop(1, 'rgba(100, 210, 255, 0.0)');

    ctx.beginPath();
    points.forEach((p, idx) => {
      if (idx === 0) ctx.moveTo(p.x, p.y);
      else ctx.lineTo(p.x, p.y);
    });
    ctx.lineTo(points[points.length - 1].x, height - padY);
    ctx.lineTo(points[0].x, height - padY);
    ctx.closePath();
    ctx.fillStyle = grad;
    ctx.fill();

    // Draw Historical Line (systemCyan dark)
    ctx.beginPath();
    ctx.strokeStyle = '#64d2ff';
    ctx.lineWidth = 2.5;
    ctx.lineCap = 'round';
    ctx.lineJoin = 'round';
    points.forEach((p, idx) => {
      if (idx === 0) ctx.moveTo(p.x, p.y);
      else ctx.lineTo(p.x, p.y);
    });
    ctx.stroke();

    // Draw Historical Dot Pills
    points.forEach(p => {
      ctx.fillStyle = '#000000';
      ctx.beginPath();
      ctx.arc(p.x, p.y, 5, 0, Math.PI * 2);
      ctx.fill();

      ctx.fillStyle = '#64d2ff';
      ctx.beginPath();
      ctx.arc(p.x, p.y, 3.5, 0, Math.PI * 2);
      ctx.fill();
    });

    // Projected Future Line (Purple Dashed)
    const lastPoint = points[points.length - 1];
    const projectX = width - padX;
    const projectBytes = lastPoint.bytes + slope * 30;
    const projectYRatio = (projectBytes - minBytes) / (maxBytes - minBytes || 1);
    const projectY = Math.max(padY, height - padY - (plotH * projectYRatio));

    ctx.save();
    ctx.setLineDash([5, 5]);
    ctx.strokeStyle = '#bf5af2';
    ctx.lineWidth = 2.5;
    ctx.beginPath();
    ctx.moveTo(lastPoint.x, lastPoint.y);
    ctx.lineTo(projectX, projectY);
    ctx.stroke();
    ctx.restore();

    // Projected Dot Pill
    ctx.fillStyle = '#000000';
    ctx.beginPath();
    ctx.arc(projectX, projectY, 6, 0, Math.PI * 2);
    ctx.fill();

    ctx.fillStyle = '#bf5af2';
    ctx.beginPath();
    ctx.arc(projectX, projectY, 4, 0, Math.PI * 2);
    ctx.fill();

    ctx.fillStyle = '#bf5af2';
    ctx.font = '600 11px Outfit, sans-serif';
    ctx.textAlign = 'right';
    ctx.fillText(`+30d Horizon (${this.formatBytes(projectBytes)})`, projectX - 12, projectY - 8);
  }

  renderAiRecommendations(dailyGrowth, daysUntilFull) {
    const container = document.getElementById('ai-recommendations-container');
    if (!container) return;

    const dupWaste = this.stats ? this.stats.total_wasted_bytes || 0 : 0;
    const staleWaste = (this.staleFiles || []).reduce((a, f) => a + (f.size || 0), 0);

    const recs = [
      {
        title: 'High-Yield Deduplication',
        desc: `Purging identical file clones will release ${this.formatBytes(dupWaste)} of disk space instantly.`,
        action: 'Review Clones',
        tab: 'duplicates'
      },
      {
        title: 'Prune Inactive Build Artifacts',
        desc: `Identified ${this.formatBytes(staleWaste)} of unreferenced cache and log files older than 30 days.`,
        action: 'Inspect Stale',
        tab: 'stale'
      },
      {
        title: 'Capacity Runway Projection',
        desc: `At current growth of ${this.formatBytes(dailyGrowth)}/day, storage exhaustion occurs in ${daysUntilFull} days.`,
        action: 'Scan Now',
        tab: 'scanner'
      }
    ];

    container.innerHTML = recs.map(r => `
      <div class="ai-rec-card">
        <div>
          <h4>${r.title}</h4>
          <p>${r.desc}</p>
        </div>
        <div>
          <button class="btn btn-sm btn-secondary" onclick="app.switchTab('${r.tab}')">${r.action}</button>
        </div>
      </div>
    `).join('');
  }

  // --- Scanner Controls ---

  setScanPath(path) {
    const input = document.getElementById('scan-path-input');
    if (input) input.value = path;
  }

  async startScan() {
    const pathInput = document.getElementById('scan-path-input');
    const workerInput = document.getElementById('scan-workers-input');
    const path = pathInput ? pathInput.value.trim() : '';
    const workers = workerInput ? parseInt(workerInput.value) || 12 : 12;

    if (!path) {
      this.showToast('Please enter or choose a valid directory path to scan.', 'warning');
      return;
    }

    try {
      const btn = document.getElementById('btn-start-scan');
      if (btn) btn.disabled = true;

      const progressBox = document.getElementById('scan-progress-box');
      if (progressBox) progressBox.style.display = 'block';

      await this.apiRequest('/scan', {
        method: 'POST',
        body: JSON.stringify({ path, workers })
      });

      this.showToast(`Scan initiated on ${path}`, 'info');
      this.pollScanStatus();
    } catch (err) {
      this.showToast(`Scan failed to start: ${err.message}`, 'error');
      const btn = document.getElementById('btn-start-scan');
      if (btn) btn.disabled = false;
    }
  }

  pollScanStatus() {
    if (this.scanPollTimer) clearInterval(this.scanPollTimer);

    this.scanPollTimer = setInterval(async () => {
      try {
        const status = await this.apiRequest('/scan/status');
        const statusText = document.getElementById('scan-status-text');
        const statsText = document.getElementById('scan-stats-text');

        if (status.status === 'scanning') {
          if (statusText) statusText.innerText = `Indexing: ${status.target_path}...`;
          if (statsText) statsText.innerText = `${(status.files_scanned || 0).toLocaleString('en-US')} files indexed (${this.formatBytes(status.total_bytes || 0)})`;
        } else {
          clearInterval(this.scanPollTimer);
          const btn = document.getElementById('btn-start-scan');
          if (btn) btn.disabled = false;

          const progressBox = document.getElementById('scan-progress-box');
          if (progressBox) progressBox.style.display = 'none';

          if (status.status === 'completed') {
            this.showToast(`Filesystem scan completed! Indexed ${(status.files_scanned || 0).toLocaleString('en-US')} files.`, 'success');
          } else if (status.status === 'failed') {
            this.showToast(`Scan failed: ${status.error || 'Unknown error'}`, 'error');
          }
          this.loadAllData();
        }
      } catch (e) {
        clearInterval(this.scanPollTimer);
      }
    }, 800);
  }

  // --- Modal & Action Execution ---

  promptTrashSelected(source) {
    const selected = this.getSelectedFiles(source);
    if (selected.length === 0) return;

    this.openModal({
      title: 'Move Files to FreeDesktop Trash',
      message: `You are about to relocate ${selected.length} file(s) (${this.formatBytes(selected.reduce((a, f) => a + f.size, 0))}) to FreeDesktop OS Native Trash.`,
      warning: 'Files can be restored to disk at any time from the Trash & Audit log or system file manager.',
      mode: 'trash',
      files: selected
    });
  }

  promptPermanentDelete(source) {
    const selected = this.getSelectedFiles(source);
    if (selected.length === 0) return;

    this.openModal({
      title: 'Permanently Destroy Files',
      message: `You are about to PERMANENTLY DESTROY ${selected.length} file(s) (${this.formatBytes(selected.reduce((a, f) => a + f.size, 0))}).`,
      warning: '⚠️ WARNING: This operation is irreversible and directly invokes os.Remove on the kernel filesystem.',
      mode: 'permanent',
      files: selected
    });
  }

  getSelectedFiles(source) {
    const selector = source === 'duplicates' ? '.dup-checkbox:checked' : '.stale-checkbox:checked';
    const checkboxes = document.querySelectorAll(selector);
    return Array.from(checkboxes).map(cb => ({
      id: parseInt(cb.getAttribute('data-id')),
      path: cb.getAttribute('data-path'),
      size: parseInt(cb.getAttribute('data-size')) || 0
    }));
  }

  openModal({ title, message, warning, mode, files }) {
    this.pendingAction = { mode, files, ids: files.map(f => f.id) };

    const titleEl = document.getElementById('modal-title');
    const msgEl = document.getElementById('modal-message');
    const warnEl = document.getElementById('modal-warning-text');
    const confirmBtn = document.getElementById('btn-modal-confirm');

    if (titleEl) titleEl.innerText = title;
    if (msgEl) msgEl.innerText = message;
    if (warnEl) warnEl.innerText = warning;

    if (confirmBtn) {
      confirmBtn.className = mode === 'permanent' ? 'btn btn-danger' : 'btn btn-primary';
      confirmBtn.innerText = mode === 'permanent' ? 'Permanently Delete' : 'Move to Trash';
    }

    const preview = document.getElementById('modal-file-list');
    if (preview) {
      preview.innerHTML = files.map(f => `<div>• ${f.path} (${this.formatBytes(f.size)})</div>`).join('');
    }

    const modal = document.getElementById('action-modal');
    if (modal) modal.style.display = 'flex';
  }

  closeModal() {
    this.pendingAction = null;
    const modal = document.getElementById('action-modal');
    if (modal) modal.style.display = 'none';
  }

  async executeConfirmedAction() {
    if (!this.pendingAction) return;

    const { mode, ids } = this.pendingAction;
    this.closeModal();

    try {
      this.showToast(`Executing ${mode} action on ${ids.length} files...`, 'info');

      const resp = await this.apiRequest('/actions', {
        method: 'POST',
        body: JSON.stringify({ ids, mode })
      });

      if (resp.success) {
        this.showToast(`Successfully processed ${resp.processed_count} files (Freed ${this.formatBytes(resp.freed_bytes)})`, 'success');
        this.loadAllData();
      } else {
        this.showToast(`Action finished with warnings.`, 'warning');
      }
    } catch (err) {
      this.showToast(`Action failed: ${err.message}`, 'error');
    }
  }

  async restoreAction(actionId) {
    try {
      this.showToast(`Restoring file from audit action #${actionId}...`, 'info');
      const resp = await this.apiRequest('/actions/restore', {
        method: 'POST',
        body: JSON.stringify({ action_id: actionId })
      });

      if (resp.success) {
        this.showToast(`Successfully restored: ${resp.restored_path}`, 'success');
        this.loadAllData();
      }
    } catch (err) {
      this.showToast(`Restore failed: ${err.message}`, 'error');
    }
  }

  // --- Utilities & Formatters ---

  renderCategoryBadge(category) {
    const map = {
      user: '<span class="badge badge-secondary">User File</span>',
      system_protected: '<span class="badge badge-purple">Protected</span>',
      system_log: '<span class="badge badge-amber">System Log</span>',
      temp: '<span class="badge badge-rose">Temp Junk</span>',
      crash_dump: '<span class="badge badge-rose">Crash Dump</span>',
      system_cache: '<span class="badge badge-green">Cache</span>'
    };
    return map[category] || `<span class="badge">${category}</span>`;
  }

  formatCategoryName(cat) {
    const map = {
      user: 'User Documents & Media',
      system_protected: 'System Protected Core',
      system_log: 'System Logs & Journals',
      temp: 'Temporary Workspaces',
      crash_dump: 'Kernel / App Crash Dumps',
      system_cache: 'Build & Application Caches'
    };
    return map[cat] || cat;
  }

  formatBytes(bytes) {
    if (!bytes || bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
  }

  formatTimestamp(val) {
    if (!val) return '--';
    let date;
    if (typeof val === 'number') {
      date = val > 1e11 ? new Date(val) : new Date(val * 1000);
    } else {
      date = new Date(val);
    }
    if (isNaN(date.getTime())) return '--';
    return date.toLocaleDateString('en-US', {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    });
  }

  showToast(message, type = 'info') {
    const container = document.getElementById('toast-container');
    if (!container) return;

    const toast = document.createElement('div');
    toast.className = `toast ${type}`;

    const icons = {
      success: '✓',
      warning: '⚠️',
      error: '✕',
      info: 'ℹ️'
    };

    toast.innerHTML = `<span>${icons[type] || 'ℹ️'}</span><span>${message}</span>`;
    container.appendChild(toast);

    setTimeout(() => {
      toast.style.opacity = '0';
      toast.style.transform = 'translateY(8px)';
      toast.style.transition = 'all 0.25s ease';
      setTimeout(() => toast.remove(), 250);
    }, 3500);
  }
}

// Global App Instance
window.app = new StorageApp();
export default window.app;
