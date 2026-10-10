/* R75 call: фон камеры в созвоне. Размытие (лёгкое, сильное) и фон BS.
   MediaPipe Tasks Vision ImageSegmenter (selfie segmentation) с нашего сервера (/vendor/mp-0.10.21/),
   кадр собирается на canvas и уходит дорожкой canvas.captureStream (не больше 24 кадров).
   Компьютер не успевает (кадр дольше 70 мс в среднем): эффект выключается сам, страница пишет почему.
   window.BSFX.start(track, mode) -> Promise<{track, mode, stop()}>; mode: none | light | strong | bg */
(function(){
'use strict';
if(window.BSFX) return;
var BASE = '/vendor/mp-0.10.21/';
var MODES = [
  {id: 'none', name: 'Без эффекта'},
  {id: 'light', name: 'Лёгкое размытие'},
  {id: 'strong', name: 'Сильное размытие'},
  {id: 'bg', name: 'Фон BS'}
];
var KEY = 'bs_fx';
var pref = function(){ try{ var v = localStorage.getItem(KEY); return MODES.some(function(m){ return m.id === v; }) ? v : 'none'; }catch(e){ return 'none'; } };
var save = function(v){ try{ localStorage.setItem(KEY, v); }catch(e){} };
var simd = function(){
  try{ return WebAssembly.validate(new Uint8Array([0,97,115,109,1,0,0,0,1,5,1,96,0,1,123,3,2,1,0,10,10,1,8,0,65,0,253,15,253,98,11])); }catch(e){ return false; }
};
var supported = function(){
  try{
    var c = document.createElement('canvas');
    return !!(window.WebAssembly && simd() && c.captureStream && c.getContext('2d'));
  }catch(e){ return false; }
};
// Сегментатор один на страницу
var segP = null, segDelegate = '';
var segmenter = function(){
  if(segP) return segP;
  segP = import(BASE + 'vision_bundle.mjs').then(function(V){
    var files = {wasmLoaderPath: location.origin + BASE + 'vision_wasm_internal.js', wasmBinaryPath: location.origin + BASE + 'vision_wasm_internal.wasm'};
    var make = function(d){
      return V.ImageSegmenter.createFromOptions(files, {baseOptions: {modelAssetPath: BASE + 'selfie_segmenter_landscape.tflite', delegate: d},
        runningMode: 'VIDEO', outputCategoryMask: false, outputConfidenceMasks: true}).then(function(s){ segDelegate = d; return s; });
    };
    return make('GPU').catch(function(){ return make('CPU'); });
  });
  segP.catch(function(){ segP = null; });
  return segP;
};
var bgImg = null;
var bg = function(){
  if(bgImg) return bgImg;
  bgImg = new Promise(function(ok){ var i = new Image(); i.onload = function(){ ok(i); }; i.onerror = function(){ ok(null); }; i.src = '/vendor/bs-bg.jpg'; });
  return bgImg;
};
var filterOK = (function(){ try{ var x = document.createElement('canvas').getContext('2d'); x.filter = 'blur(2px)'; return x.filter === 'blur(2px)'; }catch(e){ return false; } })();
var mk = function(w, h){ var c = document.createElement('canvas'); c.width = w; c.height = h; return c; };

// start: обработанная дорожка камеры. none: та же дорожка без обработки
var start = function(track, mode){
  if(!track || mode === 'none' || !MODES.some(function(m){ return m.id === mode; })) return Promise.resolve({track: track, mode: 'none', stop: function(){}, raw: track});
  if(!supported()) return Promise.reject(new Error('unsupported'));
  return Promise.all([segmenter(), mode === 'bg' ? bg() : null]).then(function(r){
    var seg = r[0], img = r[1];
    var st = track.getSettings ? track.getSettings() : {};
    var W = Math.min(640, st.width || 640), H = Math.round(W * ((st.height || 360) / (st.width || 640)));
    var v = document.createElement('video');
    v.muted = true; v.playsInline = true; v.setAttribute('playsinline', ''); v.autoplay = true;
    v.style.cssText = 'position:fixed;left:-10px;top:-10px;width:2px;height:2px;opacity:0;pointer-events:none';
    v.srcObject = new MediaStream([track]);
    document.body.appendChild(v);
    var SW = 256, SH = 144;
    var small = mk(SW, SH), sx = small.getContext('2d', {willReadFrequently: false});
    var mask = mk(SW, SH), mx = mask.getContext('2d');
    var md = mx.createImageData(SW, SH), prev = new Float32Array(SW * SH);
    var out = mk(W, H), ox = out.getContext('2d');
    var per = mk(W, H), px = per.getContext('2d');
    var k = mode === 'strong' ? 26 : 10;
    var tiny = mk(Math.max(8, Math.round(W / (mode === 'strong' ? 24 : 10))), Math.max(6, Math.round(H / (mode === 'strong' ? 24 : 10)))), tx = tiny.getContext('2d');
    var P = {mode: mode, raw: track, stopped: false, ms: 0, n: 0, onslow: null, delegate: segDelegate};
    var t0 = performance.now(), lastTs = 0, timer = 0;
    var drawBg = function(){
      if(mode === 'bg'){
        if(img){
          var s = Math.max(W / img.width, H / img.height), w = img.width * s, h = img.height * s;
          ox.drawImage(img, (W - w) / 2, (H - h) / 2, w, h);
        } else { ox.fillStyle = '#000'; ox.fillRect(0, 0, W, H); }
        return;
      }
      if(filterOK){ ox.filter = 'blur(' + k + 'px)'; ox.drawImage(v, -k, -k, W + 2 * k, H + 2 * k); ox.filter = 'none'; }
      else { tx.imageSmoothingEnabled = true; tx.drawImage(v, 0, 0, tiny.width, tiny.height); ox.imageSmoothingEnabled = true; ox.imageSmoothingQuality = 'high'; ox.drawImage(tiny, 0, 0, W, H); }
    };
    var frame = function(){
      if(P.stopped) return;
      var a = performance.now();
      if(v.readyState >= 2 && v.videoWidth){
        sx.drawImage(v, 0, 0, SW, SH);
        var ts = Math.max(lastTs + 1, Math.round(a - t0)); lastTs = ts;
        var got = false;
        try{
          seg.segmentForVideo(small, ts, function(res){
            var m = res && res.confidenceMasks && res.confidenceMasks[0];
            if(!m) return;
            var f = m.getAsFloat32Array(), d = md.data;
            for(var i = 0, j = 3; i < f.length; i++, j += 4){
              var x = f[i] * .7 + prev[i] * .3; prev[i] = x;
              // мягкий край: ниже 0.25 фон, выше 0.75 человек
              x = (x - .25) * 2; d[j] = x <= 0 ? 0 : x >= 1 ? 255 : x * 255;
            }
            got = true;
          });
        }catch(e){}
        if(got) mx.putImageData(md, 0, 0);
        drawBg();
        px.globalCompositeOperation = 'copy';
        if(filterOK) px.filter = 'blur(2px)';
        px.drawImage(mask, 0, 0, W, H);
        if(filterOK) px.filter = 'none';
        px.globalCompositeOperation = 'source-in';
        px.drawImage(v, 0, 0, W, H);
        ox.drawImage(per, 0, 0);
        var dt = performance.now() - a;
        P.ms = P.n ? P.ms * .9 + dt * .1 : dt; P.n++;
        if(P.n > 24 && P.ms > 70 && P.onslow && !P.slowTold){ P.slowTold = true; try{ P.onslow(P.ms); }catch(e){} }
      }
      timer = setTimeout(frame, Math.max(4, 1000 / 24 - (performance.now() - a)));
    };
    ox.fillStyle = '#000'; ox.fillRect(0, 0, W, H);
    var outTrack = out.captureStream(24).getVideoTracks()[0];
    try{ outTrack.contentHint = 'motion'; }catch(e){}
    P.track = outTrack;
    P.stop = function(){
      if(P.stopped) return;
      P.stopped = true; clearTimeout(timer);
      try{ outTrack.stop(); }catch(e){}
      try{ v.srcObject = null; v.remove(); }catch(e){}
    };
    track.addEventListener('ended', P.stop);
    return v.play().catch(function(){}).then(function(){
      return new Promise(function(ok){ var n = 0; (function w(){ if(v.readyState >= 2 || ++n > 40) ok(); else setTimeout(w, 50); })(); });
    }).then(function(){ frame(); return P; });
  });
};
// preload: заранее скачать модуль и модель (окно перед входом)
var preload = function(){ if(supported()) segmenter().catch(function(){}); };
window.BSFX = {MODES: MODES, pref: pref, save: save, supported: supported, start: start, preload: preload,
  name: function(id){ var m = MODES.filter(function(x){ return x.id === id; })[0]; return m ? m.name : ''; }};
})();
