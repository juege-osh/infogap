(() => {
  'use strict';
  if (document.getElementById('infogap-music')) return;
  const storageKey = 'infogap.music.v1';
  let preferences = { volume: 0.35, lyrics: false };
  try {
    const stored = JSON.parse(localStorage.getItem(storageKey) || '{}');
    if (Number.isFinite(stored.volume)) preferences.volume = Math.max(0, Math.min(1, stored.volume));
    preferences.lyrics = stored.lyrics === true;
  } catch (_) { /* Preferences are optional in private browsing. */ }
  const save = () => { try { localStorage.setItem(storageKey, JSON.stringify(preferences)); } catch (_) {} };
  const player = document.createElement('section');
  player.id = 'infogap-music';
  player.className = 'ig-music';
  player.setAttribute('aria-label', '音乐播放器');
  // Only constant markup is inserted here. Lyrics are rendered with textContent.
  player.innerHTML = `<div class="ig-music-compact"><button type="button" class="ig-music-play" aria-label="播放《先出发》" title="播放《先出发》">▶</button><div class="ig-music-title"><strong>先出发</strong><span>原创歌曲 · 三分钟版</span></div><button type="button" class="ig-music-expand" aria-label="展开音乐设置" aria-expanded="false" aria-controls="ig-music-panel">⌃</button></div><div id="ig-music-panel" class="ig-music-panel" hidden><div class="ig-music-progress"><span class="ig-music-current">0:00</span><input class="ig-music-seek" type="range" min="0" max="181.36" step="0.1" value="0" aria-label="歌曲进度"><span class="ig-music-duration">3:01</span></div><label class="ig-music-volume-label">音量 <input class="ig-music-volume" type="range" min="0" max="100" step="1" aria-label="音乐音量"><output class="ig-music-volume-value"></output></label><label class="ig-music-lyrics-label"><input class="ig-music-lyrics-toggle" type="checkbox">显示歌词</label><div class="ig-music-lyrics" hidden tabindex="0" aria-label="《先出发》歌词"></div><p class="ig-music-status" role="status" aria-live="polite"></p></div>`;
  const audio = document.createElement('audio');
  audio.preload = 'none';
  audio.src = '/media/xian-chufa.mp3';
  audio.setAttribute('aria-hidden', 'true');
  player.append(audio);
  document.body.append(player);
  document.body.classList.add('ig-music-enabled');
  const $ = name => player.querySelector('.ig-music-' + name);
  const play = $('play'), expand = $('expand'), panel = $('panel');
  const seek = $('seek'), current = $('current'), duration = $('duration');
  const volume = $('volume'), volumeValue = $('volume-value');
  const lyricsToggle = $('lyrics-toggle'), lyrics = $('lyrics'), status = $('status');
  let lyricsLoaded = false, loadingLyrics = false, seeking = false;
  let context, gain, mediaSource;
  const format = value => { const seconds = Math.max(0, Math.floor(Number(value) || 0)); return Math.floor(seconds / 60) + ':' + String(seconds % 60).padStart(2, '0'); };
  const syncVolume = () => {
    volume.value = String(Math.round(preferences.volume * 100));
    volumeValue.textContent = volume.value + '%';
    if (gain) { gain.gain.value = preferences.volume; audio.volume = 1; }
    else audio.volume = preferences.volume;
  };
  const prepareAudio = async () => {
    const AudioContext = window.AudioContext || window.webkitAudioContext;
    if (!context && AudioContext) {
      try {
        context = new AudioContext();
        mediaSource = context.createMediaElementSource(audio);
        gain = context.createGain();
        mediaSource.connect(gain);
        gain.connect(context.destination);
        syncVolume();
      } catch (_) { /* Native volume remains available if Web Audio is unavailable. */ }
    }
    if (context && context.state === 'suspended') await context.resume();
  };
  const syncPosition = () => {
    current.textContent = format(audio.currentTime);
    if (!seeking) seek.value = String(audio.currentTime || 0);
    if (Number.isFinite(audio.duration) && audio.duration > 0) {
      seek.max = String(audio.duration);
      duration.textContent = format(audio.duration);
    }
  };
  const syncPlay = () => {
    const playing = !audio.paused && !audio.ended;
    play.textContent = playing ? 'Ⅱ' : '▶';
    play.setAttribute('aria-label', (playing ? '暂停' : '播放') + '《先出发》');
    play.title = play.getAttribute('aria-label');
    player.classList.toggle('is-playing', playing);
  };
  const updateSpace = () => {
    const banner = document.getElementById('test-mode-banner');
    const bannerHeight = banner && !banner.hidden ? banner.getBoundingClientRect().height : 0;
    player.style.setProperty('--ig-music-bottom', (bannerHeight + 14) + 'px');
    document.documentElement.style.setProperty('--ig-music-reserved', Math.ceil(player.getBoundingClientRect().height + bannerHeight + 32) + 'px');
  };
  const loadLyrics = async () => {
    if (lyricsLoaded || loadingLyrics) return;
    loadingLyrics = true;
    lyrics.textContent = '正在读取歌词…';
    try {
      const response = await fetch('/media/lyrics.json');
      if (!response.ok) throw new Error('歌词暂时无法读取');
      const data = await response.json();
      if (!Array.isArray(data.lines)) throw new Error('歌词格式不正确');
      lyrics.replaceChildren();
      const note = document.createElement('p');
      note.className = 'ig-music-lyrics-note';
      note.textContent = '原始歌词，可上下滚动查看。';
      lyrics.append(note);
      for (const line of data.lines.slice(0, 300)) {
        if (!line || typeof line.text !== 'string') continue;
        const element = document.createElement('p');
        element.className = line.section ? 'ig-music-lyric-section' : 'ig-music-lyric-line';
        element.textContent = line.text || '\u00a0';
        lyrics.append(element);
      }
      lyricsLoaded = true;
    } catch (_) { lyrics.textContent = '歌词暂时无法读取，请稍后关闭再打开重试。'; }
    finally { loadingLyrics = false; updateSpace(); }
  };
  play.addEventListener('click', async () => {
    if (!audio.paused) { audio.pause(); return; }
    status.textContent = '';
    play.disabled = true;
    try {
      await prepareAudio();
      if (audio.ended) audio.currentTime = 0;
      await audio.play();
    } catch (_) {
      panel.hidden = false;
      expand.setAttribute('aria-expanded', 'true');
      expand.setAttribute('aria-label', '收起音乐设置');
      status.textContent = '暂时无法播放，请检查网络后再试。';
      updateSpace();
    } finally { play.disabled = false; syncPlay(); }
  });
  expand.addEventListener('click', () => {
    panel.hidden = !panel.hidden;
    expand.setAttribute('aria-expanded', String(!panel.hidden));
    expand.setAttribute('aria-label', panel.hidden ? '展开音乐设置' : '收起音乐设置');
    expand.textContent = panel.hidden ? '⌃' : '⌄';
    updateSpace();
  });
  volume.addEventListener('input', () => { preferences.volume = Number(volume.value) / 100; syncVolume(); save(); });
  seek.addEventListener('input', () => { seeking = true; current.textContent = format(seek.value); });
  seek.addEventListener('change', () => { if (Number.isFinite(audio.duration)) audio.currentTime = Math.min(Number(seek.value), audio.duration); seeking = false; syncPosition(); });
  lyricsToggle.checked = preferences.lyrics;
  lyrics.hidden = !preferences.lyrics;
  lyricsToggle.addEventListener('change', () => {
    preferences.lyrics = lyricsToggle.checked;
    lyrics.hidden = !preferences.lyrics;
    save();
    if (preferences.lyrics) loadLyrics();
    updateSpace();
  });
  audio.addEventListener('timeupdate', syncPosition);
  audio.addEventListener('loadedmetadata', syncPosition);
  audio.addEventListener('play', syncPlay);
  audio.addEventListener('pause', syncPlay);
  audio.addEventListener('ended', syncPlay);
  audio.addEventListener('error', () => { status.textContent = '音频暂时无法读取，请检查网络后重试。'; syncPlay(); });
  syncVolume();
  if (preferences.lyrics) loadLyrics();
  if (window.ResizeObserver) {
    const observer = new ResizeObserver(updateSpace);
    observer.observe(player);
    const banner = document.getElementById('test-mode-banner');
    if (banner) observer.observe(banner);
  }
  const banner = document.getElementById('test-mode-banner');
  if (banner) new MutationObserver(updateSpace).observe(banner, { attributes: true, attributeFilter: ['hidden', 'class'] });
  window.addEventListener('resize', updateSpace);
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && player.contains(document.activeElement) && !panel.hidden) {
      panel.hidden = true;
      expand.setAttribute('aria-expanded', 'false');
      expand.setAttribute('aria-label', '展开音乐设置');
      expand.textContent = '⌃';
      expand.focus();
      updateSpace();
    }
  });
  updateSpace();
})();
