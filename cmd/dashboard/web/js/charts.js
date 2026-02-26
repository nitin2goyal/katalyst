// ApexCharts wrapper module
const charts = {};

export function destroyCharts() {
  Object.keys(charts).forEach(k => {
    try {
      if (charts[k]) {
        // Clear SVG content from DOM first to prevent NaN attribute errors
        // from in-flight animation frames that execute after destroy()
        const el = charts[k].el;
        if (el) el.innerHTML = '';
        if (typeof charts[k].destroy === 'function') {
          charts[k].destroy();
        }
      }
    } catch (_) { /* ignore teardown errors */ }
    delete charts[k];
  });
}

function getThemeColors() {
  const isDark = document.documentElement.getAttribute('data-theme') !== 'light';
  return {
    isDark,
    text: isDark ? '#c9cdd8' : '#334155',
    muted: isDark ? '#6b7280' : '#94a3b8',
    grid: isDark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.06)',
    cardBg: isDark ? '#141425' : '#ffffff',
    trackBg: isDark ? 'rgba(255,255,255,0.06)' : '#e2e8f0',
  };
}

function ensureDiv(containerId) {
  const el = document.getElementById(containerId);
  if (!el) return null;
  if (el.tagName === 'CANVAS') {
    const div = document.createElement('div');
    div.id = containerId;
    el.parentNode.replaceChild(div, el);
    return div;
  }
  return el;
}

function baseChart(height) {
  return {
    fontFamily: "'DM Sans', sans-serif",
    toolbar: { show: false },
    background: 'transparent',
    animations: { enabled: true, easing: 'easeinout', speed: 700, animateGradually: { enabled: true, delay: 80 } },
    height: height || '100%',
    dropShadow: { enabled: false },
  };
}

function baseGrid() {
  const t = getThemeColors();
  return {
    borderColor: t.grid,
    strokeDashArray: 4,
    xaxis: { lines: { show: false } },
    yaxis: { lines: { show: true } },
    padding: { top: 0, right: 8, bottom: 0, left: 8 },
  };
}

function baseAxis() {
  const t = getThemeColors();
  return {
    xaxis: {
      labels: { style: { colors: t.muted, fontSize: '10px', fontWeight: 500 } },
      axisBorder: { show: false },
      axisTicks: { show: false },
    },
    yaxis: {
      labels: {
        style: { colors: t.muted, fontSize: '10px', fontWeight: 500 },
        formatter: (val) => {
          if (val >= 1000) return '$' + (val / 1000).toFixed(1) + 'k';
          return '$' + Number(val).toFixed(0);
        },
      },
    },
  };
}

function renderApex(containerId, cfg) {
  if (charts[containerId]) {
    charts[containerId].destroy();
    delete charts[containerId];
  }
  const el = ensureDiv(containerId);
  if (!el) return null;
  const chart = new ApexCharts(el, cfg);
  chart.render();
  charts[containerId] = chart;
  return chart;
}

// ─── Public API ───

export function makeBarChart(containerId, { categories, series, colors, horizontal, stacked, columnWidth, formatter, noCurrency }) {
  const t = getThemeColors();
  const ax = baseAxis();
  const distributed = series.length === 1 && Array.isArray(colors) && colors.length > 1;
  const fmtVal = noCurrency
    ? (val) => { if (val >= 1000) return (val / 1000).toFixed(1) + 'k'; return Number(val).toFixed(0); }
    : null;

  return renderApex(containerId, {
    chart: { ...baseChart(), type: 'bar', stacked: !!stacked },
    series,
    colors: distributed ? colors : (colors || ['#6366f1']),
    plotOptions: {
      bar: {
        horizontal: !!horizontal,
        borderRadius: horizontal ? 3 : 4,
        borderRadiusApplication: 'end',
        columnWidth: columnWidth || (series.length > 1 ? '55%' : '45%'),
        distributed,
        dataLabels: { position: 'top' },
      },
    },
    dataLabels: {
      enabled: horizontal && series.length === 1,
      formatter: formatter || fmtVal || ((val) => '$' + Number(val).toFixed(0)),
      offsetX: 8,
      style: { fontSize: '11px', fontWeight: 600, colors: [t.text] },
    },
    grid: baseGrid(),
    xaxis: { ...ax.xaxis, categories },
    yaxis: horizontal
      ? { ...ax.yaxis, labels: { ...ax.yaxis.labels, formatter: (v) => v } }
      : noCurrency
        ? { ...ax.yaxis, labels: { ...ax.yaxis.labels, formatter: fmtVal } }
        : ax.yaxis,
    legend: { show: series.length > 1, fontSize: '11px', labels: { colors: t.text }, position: 'top', horizontalAlign: 'left', markers: { radius: 3 } },
    tooltip: {
      theme: t.isDark ? 'dark' : 'light',
      style: { fontSize: '11px' },
      y: { formatter: formatter || fmtVal || ((val) => '$' + Number(val).toFixed(2)) },
    },
  });
}

export function makeAreaChart(containerId, { categories, series, colors, gradient }) {
  const t = getThemeColors();
  const ax = baseAxis();
  const chartColors = colors || ['#6366f1'];

  return renderApex(containerId, {
    chart: { ...baseChart(), type: 'area' },
    series,
    colors: chartColors,
    stroke: { curve: 'smooth', width: 2.5, lineCap: 'round' },
    fill: {
      type: 'gradient',
      gradient: gradient || {
        shadeIntensity: 1,
        type: 'vertical',
        opacityFrom: 0.35,
        opacityTo: 0.02,
        stops: [0, 100],
        colorStops: chartColors.map(c => ([
          { offset: 0, color: c, opacity: 0.3 },
          { offset: 100, color: c, opacity: 0.02 },
        ])),
      },
    },
    grid: baseGrid(),
    xaxis: {
      ...ax.xaxis,
      categories,
      labels: { ...ax.xaxis.labels, rotate: -45, rotateAlways: false, hideOverlappingLabels: true, maxHeight: 60 },
    },
    yaxis: ax.yaxis,
    legend: { show: series.length > 1, fontSize: '11px', labels: { colors: t.text }, position: 'top', horizontalAlign: 'left', markers: { radius: 3 } },
    tooltip: {
      theme: t.isDark ? 'dark' : 'light',
      style: { fontSize: '11px' },
      x: { show: true },
      y: { formatter: (val) => '$' + Number(val).toFixed(2) },
    },
    markers: { size: 0, hover: { size: 5, sizeOffset: 2 } },
  });
}

export function makeDonutChart(containerId, { labels, series, colors }) {
  const t = getThemeColors();

  return renderApex(containerId, {
    chart: { ...baseChart(), type: 'donut' },
    series,
    labels,
    colors: colors || ['#6366f1', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#64748b'],
    plotOptions: {
      pie: {
        donut: {
          size: '72%',
          labels: {
            show: true,
            name: { show: true, fontSize: '12px', color: t.muted, offsetY: 20 },
            value: { show: true, fontSize: '22px', fontWeight: 700, color: t.text, offsetY: -12, formatter: (val) => '$' + Math.round(Number(val)).toLocaleString() },
            total: {
              show: true,
              label: 'Total',
              fontSize: '11px',
              color: t.muted,
              formatter: (w) => '$' + Math.round(w.globals.seriesTotals.reduce((a, b) => a + b, 0)).toLocaleString(),
            },
          },
        },
      },
    },
    dataLabels: { enabled: false },
    legend: {
      show: true, position: 'bottom', fontSize: '11px', labels: { colors: t.text }, markers: { radius: 3 },
      formatter: (label, opts) => label + ' - $' + Math.round(opts.w.globals.series[opts.seriesIndex]).toLocaleString(),
    },
    stroke: { show: true, width: 2, colors: [t.isDark ? '#141425' : '#ffffff'] },
    tooltip: {
      theme: t.isDark ? 'dark' : 'light',
      y: { formatter: (val) => '$' + Math.round(val).toLocaleString() },
    },
  });
}

export function makeRadialChart(containerId, { value, label, color }) {
  const t = getThemeColors();
  const c = color || (value < 50 ? '#10b981' : value < 80 ? '#f59e0b' : '#ef4444');

  return renderApex(containerId, {
    chart: { ...baseChart(), type: 'radialBar', sparkline: { enabled: false } },
    series: [Math.round(value)],
    colors: [c],
    plotOptions: {
      radialBar: {
        startAngle: -135,
        endAngle: 135,
        hollow: { size: '62%', background: 'transparent' },
        track: {
          background: t.trackBg,
          strokeWidth: '100%',
          margin: 0,
          dropShadow: { enabled: false },
        },
        dataLabels: {
          name: { show: true, offsetY: 22, color: t.muted, fontSize: '11px', fontWeight: 500 },
          value: { show: true, offsetY: -14, color: t.text, fontSize: '26px', fontWeight: 700, formatter: (val) => val + '%' },
        },
      },
    },
    labels: [label],
    stroke: { lineCap: 'round' },
  });
}

// ─── Legacy compatibility (converts old Chart.js-style calls) ───

export function makeChart(containerId, cfg) {
  const type = cfg.type;
  const data = cfg.data || {};
  const labels = data.labels || [];
  const datasets = data.datasets || [];
  const opts = cfg.options || {};

  if (type === 'bar') {
    const isHorizontal = opts.indexAxis === 'y';
    return makeBarChart(containerId, {
      categories: labels,
      series: datasets.map(ds => ({ name: ds.label || '', data: ds.data || [] })),
      colors: datasets.length === 1
        ? (Array.isArray(datasets[0].backgroundColor) ? datasets[0].backgroundColor : [datasets[0].backgroundColor || '#6366f1'])
        : datasets.map(ds => Array.isArray(ds.backgroundColor) ? ds.backgroundColor[0] : ds.backgroundColor || '#6366f1'),
      horizontal: isHorizontal,
      noCurrency: cfg.noCurrency,
    });
  }

  if (type === 'line') {
    return makeAreaChart(containerId, {
      categories: labels,
      series: datasets.map(ds => ({ name: ds.label || '', data: ds.data || [] })),
      colors: datasets.map(ds => ds.borderColor || '#6366f1'),
    });
  }

  if (type === 'doughnut' || type === 'pie') {
    const ds = datasets[0] || {};
    return makeDonutChart(containerId, {
      labels,
      series: ds.data || [],
      colors: ds.backgroundColor,
    });
  }

  return null;
}

export function renderGauge(containerId, pct, label) {
  return makeRadialChart(containerId, { value: pct, label });
}
