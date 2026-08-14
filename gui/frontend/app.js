// Intelligent Storage Optimizer - Frontend Application Logic
// Communicates with Go Core REST API (http://127.0.0.1:8080/api/v1)

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
    
    // Modal State
    this.pendingAction = null; // { mode: 'trash'|'permanent', ids: [], files: [] }

    this.init();
  }

  init() {
    this.setupNavigation();
    this.checkApiHealth();
    this.healthPollTimer = setInterval(() => this.checkApiHealth(), 10000);
    this.loadAllData();
    this.setupEventListeners();
  }

  setupNavigation() {
    document.querySelectorAll('.nav-item').forEach(button => {
      button.addEventListener('click', (e) => {
        const tab = button.getAttribute('data-tab');
        this.switchTab(tab);
      });
    });
  }

  setupEventListeners() {
    document.getElementById('btn-refresh-all')?.addEventListener('click', () => {
      this.showToast('Refreshing all system metrics...', 'info');
      this.loadAllData();
    });

    document.getElementById('btn-quick-scan')?.addEventListener('click', () => {
      this.switchTab('scanner');
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
      scanner: { title: 'Filesystem Scanner & Indexer', sub: 'Concurrent POSIX metadata indexing and snapshot tracking' },
      duplicates: { title: 'Duplicate File Hunter', sub: 'Two-pass cryptographic deduplication and space reclamation' },
      stale: { title: 'Stale & Inactive Junk Files', sub: 'Mathematical staleness ranking and system log/cache identification' },
      forecasting: { title: 'AI Storage Forecasting (ML Layer)', sub: 'Time-series growth trajectory, days-until-full estimation, and smart recommendations' },
      audit: { title: 'FreeDesktop XDG Trash & Audit Log', sub: 'Immutable audit trail of past file cleanups and instant file restoration' }
    };

    const t = titles[tabId] || titles.dashboard;
    document.getElementById('page-title').innerText = t.title;
    document.getElementById('page-subtitle').innerText = t.sub;

    // Trigger tab-specific refresh if needed
    if (tabId === 'forecasting') {
      this.renderForecastView();
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
        dot.className = 'status-indicator-dot online';
        title.innerText = 'Go Core Connected';
        sub.innerText = '127.0.0.1:8080 (Active)';
      }
    } catch (err) {
      dot.className = 'status-indicator-dot';
      title.innerText = 'Core Disconnected';
      sub.innerText = 'Start ./bin/storage-optimizer serve';
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
    } catch (e) {
      console.error('Error during data load:', e);
    }
  }

  async loadStats() {
    try {
      const stats = await this.apiRequest('/stats');
      this.stats = stats;
      this.renderDashboardStats(stats);
      this.renderCategoryChart(stats.categories, stats.total_bytes);
    } catch (err) {
      console.error('Failed to load stats:', err);
    }
  }

  async loadDuplicates() {
    try {
      const data = await this.apiRequest('/files/duplicates');
      this.duplicates = data.groups || [];
      const badge = document.getElementById('badge-duplicates');
      if (badge) badge.innerText = this.duplicates.length;
      const summaryBadge = document.getElementById('dup-summary-badge');
      if (summaryBadge) summaryBadge.innerText = `${this.duplicates.length} Duplicate Groups (${this.formatBytes(data.total_wasted_bytes || 0)} wasted)`;
      this.renderDuplicatesList();
    } catch (err) {
      console.error('Failed to load duplicates:', err);
    }
  }

  async loadStaleFiles(days = 30) {
    try {
      // Update active filter button state
      document.querySelectorAll('.btn-filter').forEach(b => {
        b.classList.toggle('active', parseInt(b.getAttribute('data-days')) === days);
      });

      const data = await this.apiRequest(`/files/stale?days=${days}&min_score=0.10&limit=100`);
      this.staleFiles = data.files || [];
      const badge = document.getElementById('badge-stale');
      if (badge) badge.innerText = this.staleFiles.length;
      this.renderStaleTable();
    } catch (err) {
      console.error('Failed to load stale files:', err);
    }
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
      if (badge) badge.innerText = `${this.auditLogs.length} Log Entries`;
      this.renderAuditTable();
    } catch (err) {
      console.error('Failed to load audit logs:', err);
    }
  }

  // --- Rendering Functions ---

  renderDashboardStats(stats) {
    document.getElementById('stat-total-storage').innerText = this.formatBytes(stats.total_bytes);
    document.getElementById('stat-total-files').innerText = `${stats.total_files.toLocaleString()} files indexed`;

    document.getElementById('stat-duplicate-bytes').innerText = this.formatBytes(stats.total_wasted_bytes);
    document.getElementById('stat-duplicate-count').innerText = `${(stats.total_duplicates || 0).toLocaleString()} redundant files`;

    // Compute stale sum
    const staleBytes = this.staleFiles.reduce((acc, f) => acc + (f.size || 0), 0);
    document.getElementById('stat-stale-bytes').innerText = this.formatBytes(staleBytes);
    document.getElementById('stat-stale-count').innerText = `${this.staleFiles.length} files (30+ days)`;

    const reclaimable = (stats.total_wasted_bytes || 0) + staleBytes;
    document.getElementById('stat-reclaimable-total').innerText = this.formatBytes(reclaimable);
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
      user: '#38bdf8',              // Electric Blue
      system_protected: '#a855f7',  // Purple
      system_log: '#f59e0b',        // Amber
      temp: '#ef4444',              // Red/Rose
      crash_dump: '#f97316',        // Orange
      system_cache: '#10b981'       // Emerald
    };

    const circumference = 2 * Math.PI * 38; // r=38 -> ~238.76
    let accumulatedOffset = 0;

    categories.forEach(cat => {
      const fraction = totalBytes > 0 ? cat.total_bytes / totalBytes : 0;
      const strokeLength = fraction * circumference;
      const strokeSpace = circumference - strokeLength;
      const color = colors[cat.category] || '#94a3b8';

      // SVG Donut slice
      if (fraction > 0.001) {
        const circle = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
        circle.setAttribute('class', 'donut-segment');
        circle.setAttribute('cx', '50');
        circle.setAttribute('cy', '50');
        circle.setAttribute('r', '38');
        circle.setAttribute('fill', 'transparent');
        circle.setAttribute('stroke', color);
        circle.setAttribute('stroke-width', '12');
        circle.setAttribute('stroke-dasharray', `${strokeLength} ${strokeSpace}`);
        circle.setAttribute('stroke-dashoffset', `${-accumulatedOffset}`);
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
        <span class="legend-value">${this.formatBytes(cat.total_bytes)} (${cat.total_files.toLocaleString()})</span>
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
          <div class="empty-icon">🎉</div>
          <h3>Zero Duplicates Found</h3>
          <p>Your storage index is clean and free of redundant identical files.</p>
        </div>
      `;
      return;
    }

    container.innerHTML = this.duplicates.map((group, groupIdx) => {
      const wasted = group.wasted_bytes;
      return `
        <div class="duplicate-cluster-card">
          <div class="cluster-header">
            <div class="cluster-hash">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>
              <span>SHA-256: ${group.hash.substring(0, 16)}...</span>
            </div>
            <div class="cluster-meta">
              <span>${group.files.length} Copies</span> • <span class="text-warning">${this.formatBytes(wasted)} Reclaimable</span>
            </div>
          </div>
          <div class="cluster-files-list">
            ${group.files.map((file, fileIdx) => {
              const isFirst = fileIdx === 0;
              return `
                <div class="duplicate-item-row ${isFirst ? 'is-original' : ''}">
                  <input type="checkbox" class="dup-checkbox" data-id="${file.id}" data-path="${file.path}" data-size="${file.size}" ${!isFirst ? 'checked' : ''} onchange="app.updateDuplicateActionButtons()">
                  <span class="path-cell">${file.path}</span>
                  <span class="size-cell">${this.formatBytes(file.size)}</span>
                  ${isFirst ? '<span class="badge badge-emerald">Original (Keep)</span>' : '<span class="badge badge-amber">Duplicate Copy</span>'}
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
    document.querySelectorAll('.dup-checkbox').forEach((cb, idx) => {
      // Keep first copy unchecked, select remainder
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
      tbody.innerHTML = '<tr><td colspan="6" class="empty-cell">No stale or inactive files found for this threshold.</td></tr>';
      return;
    }

    tbody.innerHTML = this.staleFiles.map(file => {
      const scorePct = Math.round((file.staleness_score || 0) * 100);
      const scoreColor = scorePct > 80 ? 'var(--accent-rose)' : scorePct > 50 ? 'var(--accent-amber)' : 'var(--accent-blue)';
      const isProtected = file.category === 'system_protected';

      return `
        <tr>
          <td><input type="checkbox" class="stale-checkbox" data-id="${file.id}" data-path="${file.path}" data-size="${file.size}" data-cat="${file.category}" ${isProtected ? 'disabled' : ''} onchange="app.updateStaleActionButtons()"></td>
          <td>
            <div class="staleness-meter"><div class="staleness-fill" style="width: ${scorePct}%; background: ${scoreColor}"></div></div>
            <span style="font-family: var(--font-mono); font-size: 0.8rem">${(file.staleness_score || 0).toFixed(2)}</span>
          </td>
          <td>${this.renderCategoryBadge(file.category)}</td>
          <td class="path-cell" title="${file.path}">${file.path}</td>
          <td class="size-cell">${this.formatBytes(file.size)}</td>
          <td style="color: var(--text-dim); font-size: 0.78rem;">${this.formatTimestamp(file.atime || file.mtime)}</td>
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
    this.showToast('Selected all non-system stale files.', 'info');
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
        <td><span class="badge">#${s.id}</span></td>
        <td>${this.formatTimestamp(s.scanned_at)}</td>
        <td class="path-cell">${s.root_path}</td>
        <td>${(s.total_files || 0).toLocaleString()}</td>
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
          <td><span class="badge">#${log.id}</span></td>
          <td><span class="badge ${isTrash ? 'badge-amber' : 'badge-rose'}">${log.action_mode.toUpperCase()}</span></td>
          <td class="path-cell" title="${log.file_path}">${log.file_path}</td>
          <td class="path-cell" style="color: var(--text-dim);">${log.trashed_to_path || 'None (Removed)'}</td>
          <td class="size-cell">${this.formatBytes(log.file_size)}</td>
          <td>${this.formatTimestamp(log.performed_at)}</td>
          <td>
            ${isTrash ? `<button class="btn btn-sm btn-primary" onclick="app.restoreAction(${log.id})">Restore</button>` : '<span style="color: var(--text-dim); font-size: 0.75rem;">Permanent</span>'}
          </td>
        </tr>
      `;
    }).join('');
  }

  // --- AI Forecasting & ML View (Sahil's View) ---

  renderForecastView() {
    const canvas = document.getElementById('forecast-canvas');
    if (!canvas) return;

    const ctx = canvas.getContext('2d');
    const width = canvas.width;
    const height = canvas.height;

    ctx.clearRect(0, 0, width, height);

    if (this.snapshots.length < 2) {
      // If we don't have enough snapshots, synthesize regression with current stats
      this.drawEmptyForecast(ctx, width, height);
      return;
    }

    // Sort snapshots chronologically
    const sorted = [...this.snapshots].sort((a, b) => a.scanned_at - b.scanned_at);
    const times = sorted.map(s => s.scanned_at);
    const bytes = sorted.map(s => s.total_bytes);

    // Simple Linear Regression: y = m*x + c
    const n = sorted.length;
    const minT = times[0];
    const maxT = times[n - 1];
    const timeSpan = maxT - minT || 86400; // avoid 0

    let sumX = 0, sumY = 0, sumXY = 0, sumXX = 0;
    for (let i = 0; i < n; i++) {
      const x = (times[i] - minT) / 86400; // days from start
      const y = bytes[i];
      sumX += x;
      sumY += y;
      sumXY += x * y;
      sumXX += x * x;
    }

    const slope = (n * sumXY - sumX * sumY) / (n * sumXX - sumX * sumX || 1); // bytes per day
    const intercept = (sumY - slope * sumX) / n;

    // Daily growth metric
    const dailyGrowth = Math.max(slope, 1024 * 1024); // at least 1MB/day baseline
    document.getElementById('forecast-daily-growth').innerText = `${this.formatBytes(dailyGrowth)} / day`;

    // Estimate Days until disk full (assuming standard 100GB or 500GB volume for demo)
    const currentBytes = bytes[bytes.length - 1];
    const assumedCapacity = Math.max(currentBytes * 1.5, 100 * 1024 * 1024 * 1024);
    const remainingBytes = assumedCapacity - currentBytes;
    const daysUntilFull = Math.max(Math.round(remainingBytes / dailyGrowth), 14);

    document.getElementById('forecast-days-until-full').innerText = `${daysUntilFull} Days`;

    // Draw Smooth Canvas Line Chart
    this.drawForecastChart(ctx, width, height, sorted, slope, intercept, minT, assumedCapacity);

    // Render AI Recommendation Cards
    this.renderAiRecommendations(dailyGrowth, daysUntilFull);
  }

  drawEmptyForecast(ctx, width, height) {
    ctx.fillStyle = 'rgba(255, 255, 255, 0.03)';
    ctx.fillRect(0, 0, width, height);

    ctx.fillStyle = '#64748b';
    ctx.font = '14px Inter, sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('Execute multiple scans across directories to record time-series snapshots for ML modeling.', width / 2, height / 2);
  }

  drawForecastChart(ctx, width, height, snapshots, slope, intercept, minT, capacity) {
    const padX = 60;
    const padY = 40;
    const plotW = width - padX * 2;
    const plotH = height - padY * 2;

    const maxBytes = Math.max(...snapshots.map(s => s.total_bytes)) * 1.25;
    const minBytes = Math.min(...snapshots.map(s => s.total_bytes)) * 0.85;

    // Draw Grid Lines
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.05)';
    ctx.lineWidth = 1;
    for (let i = 0; i <= 4; i++) {
      const y = padY + (plotH / 4) * i;
      ctx.beginPath();
      ctx.moveTo(padX, y);
      ctx.lineTo(width - padX, y);
      ctx.stroke();

      const labelVal = maxBytes - (i / 4) * (maxBytes - minBytes);
      ctx.fillStyle = '#64748b';
      ctx.font = '10px JetBrains Mono';
      ctx.textAlign = 'right';
      ctx.fillText(this.formatBytes(labelVal), padX - 10, y + 3);
    }

    // Historical Points Path
    const points = snapshots.map((s, i) => {
      const xRatio = snapshots.length > 1 ? i / (snapshots.length - 1) : 0;
      const x = padX + (plotW * 0.6) * xRatio;
      const yRatio = (s.total_bytes - minBytes) / (maxBytes - minBytes);
      const y = height - padY - (plotH * yRatio);
      return { x, y, bytes: s.total_bytes, time: s.scanned_at };
    });

    // Draw Historical Line (Cyan)
    ctx.beginPath();
    ctx.strokeStyle = '#00f0ff';
    ctx.lineWidth = 3;
    points.forEach((p, idx) => {
      if (idx === 0) ctx.moveTo(p.x, p.y);
      else ctx.lineTo(p.x, p.y);
    });
    ctx.stroke();

    // Draw Historical Dots
    points.forEach(p => {
      ctx.fillStyle = '#00f0ff';
      ctx.beginPath();
      ctx.arc(p.x, p.y, 5, 0, Math.PI * 2);
      ctx.fill();
    });

    // Projected Future Line (Purple Dashed)
    const lastPoint = points[points.length - 1];
    const projectX = width - padX;
    const projectBytes = lastPoint.bytes + slope * 30; // 30 day forecast
    const projectYRatio = (projectBytes - minBytes) / (maxBytes - minBytes);
    const projectY = Math.max(padY, height - padY - (plotH * projectYRatio));

    ctx.save();
    ctx.setLineDash([6, 6]);
    ctx.strokeStyle = '#a855f7';
    ctx.lineWidth = 2.5;
    ctx.beginPath();
    ctx.moveTo(lastPoint.x, lastPoint.y);
    ctx.lineTo(projectX, projectY);
    ctx.stroke();
    ctx.restore();

    // Projected Endpoint Indicator
    ctx.fillStyle = '#a855f7';
    ctx.beginPath();
    ctx.arc(projectX, projectY, 6, 0, Math.PI * 2);
    ctx.fill();

    ctx.fillStyle = '#a855f7';
    ctx.font = '11px Outfit, sans-serif';
    ctx.textAlign = 'left';
    ctx.fillText(`+30d Forecast (${this.formatBytes(projectBytes)})`, projectX - 120, projectY - 10);
  }

  renderAiRecommendations(dailyGrowth, daysUntilFull) {
    const container = document.getElementById('ai-recommendations-container');
    if (!container) return;

    const dupWaste = this.stats ? this.stats.total_wasted_bytes || 0 : 0;
    const staleWaste = this.staleFiles.reduce((a, f) => a + (f.size || 0), 0);

    const recs = [
      {
        title: 'High-Impact Duplicate Reclamation',
        desc: `Purging identical file clones will instantly release ${this.formatBytes(dupWaste)} without any data loss.`,
        action: 'Review Duplicates',
        tab: 'duplicates',
        color: 'purple'
      },
      {
        title: 'Inactive Build Cache Clean',
        desc: `Identified ${this.formatBytes(staleWaste)} of logs and caches inactive for over 30 days.`,
        action: 'Inspect Stale Junk',
        tab: 'stale',
        color: 'amber'
      },
      {
        title: 'Disk Capacity Runway',
        desc: `At current growth rate of ${this.formatBytes(dailyGrowth)}/day, storage limit will be reached in ${daysUntilFull} days.`,
        action: 'Configure Alerts',
        tab: 'scanner',
        color: 'cyan'
      }
    ];

    container.innerHTML = recs.map(r => `
      <div class="ai-rec-card">
        <div>
          <h4>${r.title}</h4>
          <p>${r.desc}</p>
        </div>
        <div class="ai-rec-footer">
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
      this.showToast('Please enter a valid directory path to scan.', 'warning');
      return;
    }

    try {
      const btn = document.getElementById('btn-start-scan');
      if (btn) btn.disabled = true;

      const progressBox = document.getElementById('scan-progress-box');
      if (progressBox) progressBox.style.display = 'block';

      const resp = await this.apiRequest('/scan', {
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

        if (status.is_scanning) {
          if (statusText) statusText.innerText = `Scanning: ${status.target_path}...`;
          if (statsText) statsText.innerText = `${status.files_scanned.toLocaleString()} files scanned`;
        } else {
          // Finished
          clearInterval(this.scanPollTimer);
          const btn = document.getElementById('btn-start-scan');
          if (btn) btn.disabled = false;

          const progressBox = document.getElementById('scan-progress-box');
          if (progressBox) progressBox.style.display = 'none';

          this.showToast('Filesystem scan & indexing completed!', 'success');
          this.loadAllData();
        }
      } catch (e) {
        clearInterval(this.scanPollTimer);
      }
    }, 800);
  }

  // --- Action & Deletion Modal Engine ---

  promptTrashSelected(source) {
    const selected = this.getSelectedFiles(source);
    if (selected.length === 0) return;

    this.openModal({
      title: 'Move Selected Files to OS Trash',
      message: `You are about to relocate ${selected.length} file(s) (${this.formatBytes(selected.reduce((a, f) => a + f.size, 0))}) to FreeDesktop.org OS Native Trash.`,
      warning: 'Files can be restored to disk at any time from the Trash & Audit log or file manager.',
      mode: 'trash',
      files: selected
    });
  }

  promptPermanentDelete(source) {
    const selected = this.getSelectedFiles(source);
    if (selected.length === 0) return;

    this.openModal({
      title: 'Permanently Destroy Selected Files',
      message: `You are about to PERMANENTLY DELETE ${selected.length} file(s) (${this.formatBytes(selected.reduce((a, f) => a + f.size, 0))}).`,
      warning: '⚠️ WARNING: This operation is irreversible and directly executes os.Remove on the kernel filesystem.',
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

    document.getElementById('modal-title').innerText = title;
    document.getElementById('modal-message').innerText = message;
    document.getElementById('modal-warning-text').innerText = warning;

    const preview = document.getElementById('modal-file-list');
    preview.innerHTML = files.map(f => `<div>• ${f.path} (${this.formatBytes(f.size)})</div>`).join('');

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
      }
    } catch (err) {
      this.showToast(`Cleanup action failed: ${err.message}`, 'error');
    }
  }

  async restoreAction(actionId) {
    try {
      this.showToast(`Restoring file for Action #${actionId}...`, 'info');
      const resp = await this.apiRequest(`/actions/restore?id=${actionId}`, { method: 'POST' });
      if (resp.success) {
        this.showToast(`Restored: ${resp.restored.file_path}`, 'success');
        this.loadAllData();
      }
    } catch (err) {
      this.showToast(`Restoration failed: ${err.message}`, 'error');
    }
  }

  // --- UI Helpers ---

  formatBytes(bytes) {
    if (!bytes || bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
  }

  formatTimestamp(unixSec) {
    if (!unixSec) return '--';
    const date = new Date(unixSec * 1000);
    return date.toLocaleDateString() + ' ' + date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  formatCategoryName(cat) {
    const names = {
      user: 'User Documents & Media',
      system_protected: 'OS Core (Protected)',
      system_log: 'System & Git Logs',
      temp: 'Temporary Files',
      crash_dump: 'Crash Dumps',
      system_cache: 'Caches & Node Modules'
    };
    return names[cat] || cat;
  }

  renderCategoryBadge(cat) {
    const badges = {
      user: '<span class="badge">User</span>',
      system_protected: '<span class="badge badge-purple">Protected</span>',
      system_log: '<span class="badge badge-amber">Log</span>',
      temp: '<span class="badge badge-rose">Temp</span>',
      crash_dump: '<span class="badge badge-amber">Crash Dump</span>',
      system_cache: '<span class="badge badge-emerald">Cache</span>'
    };
    return badges[cat] || `<span class="badge">${cat}</span>`;
  }

  showToast(message, type = 'info') {
    const container = document.getElementById('toast-container');
    if (!container) return;

    const toast = document.createElement('div');
    toast.className = `toast ${type}`;
    toast.innerHTML = `<span>${message}</span>`;
    container.appendChild(toast);

    setTimeout(() => {
      toast.style.opacity = '0';
      toast.style.transform = 'translateX(100%)';
      setTimeout(() => toast.remove(), 300);
    }, 3500);
  }
}

// Global instance for inline button callbacks
const app = new StorageApp();
window.app = app;
