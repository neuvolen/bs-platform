/* BS R79: «Джарвис» в браузере. Web Worker: звук микрофона (16 кГц, моно) приходит сюда и никуда
   не уходит. Маленькая русская модель (Vosk small streaming Zipformer, sherpa-onnx в WebAssembly)
   ищет слово-обращение двумя способами: поиск ключевого слова (KWS) и черновая расшифровка с
   нечётким сравнением («джервис», «жарвис», «джавис»). Сработало: странице приходит {type:'wake'}.
   Во время команды та же модель пишет черновой текст команды ({type:'final'} по запросу страницы). */
'use strict';
var M = null, kws = null, kst = null, rec = null, rst = null, mode = 'wake', ready = false, lastText = '', sinceReset = 0;
var WAKE = ['джарвис', 'жарвис', 'джервис', 'джавис', 'ассистент', 'асистент'];
// токены модели (bpe.model Vosk small ru): «джарвис» = ▁д жа р ви с и т. п.
var KW = ['▁д жа р ви с @джарвис', '▁ жа р ви с @жарвис', '▁д же р ви с @джервис', '▁д жа р ви з @джарвиз', '▁а сси ст ент @ассистент', '▁а си ст ент @асистент'];

function post(m){ try{ self.postMessage(m); }catch(e){} }
function lev(a, b){
  var m = a.length, n = b.length, d = [], i, j;
  for(i = 0; i <= m; i++){ d[i] = [i]; }
  for(j = 1; j <= n; j++) d[0][j] = j;
  for(i = 1; i <= m; i++) for(j = 1; j <= n; j++) d[i][j] = Math.min(d[i-1][j] + 1, d[i][j-1] + 1, d[i-1][j-1] + (a[i-1] === b[j-1] ? 0 : 1));
  return d[m][n];
}
// слово-обращение в черновом тексте: расстояние 1 (2 для длинных слов)
function wakeIn(t){
  var ws = String(t || '').toLowerCase().replace(/ё/g, 'е').split(/[^а-яa-z]+/);
  for(var i = 0; i < ws.length; i++){
    var w = ws[i]; if(w.length < 5) continue;
    if(w === 'jarvis') return w;
    for(var k = 0; k < WAKE.length; k++){ if(lev(w, WAKE[k]) <= (WAKE[k].length >= 8 ? 2 : 1)) return w; }
  }
  return '';
}
function api(src, names){ return new Function(src + '\nreturn {' + names.map(function(n){ return n + ':' + n; }).join(',') + '};')(); }
function get(url, bin){
  return fetch(url, {credentials: 'same-origin'}).then(function(r){
    if(!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
    return bin ? r.arrayBuffer() : r.text();
  });
}

function init(o){
  var base = o.base || '/vendor/kws-1.13.8/', mb = o.model || '/vendor/kws-model/';
  importScripts(base + 'sherpa-onnx-wasm.js');
  var mod = {locateFile: function(p){ return base + p; }, print: function(){}, printErr: function(){}};
  var files = ['encoder.onnx', 'decoder.onnx', 'joiner.onnx', 'tokens.txt'];
  return Promise.all([
    Module(mod),
    get(base + 'sherpa-onnx-kws.js'), get(base + 'sherpa-onnx-asr.js'),
    Promise.all(files.map(function(f){ return get(mb + f, true).then(function(b){ post({type: 'progress', file: f}); return b; }); }))
  ]).then(function(r){
    M = mod;
    var K = api(r[1], ['createKws']), A = api(r[2], ['createOnlineRecognizer']), bufs = r[3];
    try{ M.FS.mkdir('/m'); }catch(e){}
    files.forEach(function(f, i){ M.FS.writeFile('/m/' + f, new Uint8Array(bufs[i])); });
    var tr = {encoder: '/m/encoder.onnx', decoder: '/m/decoder.onnx', joiner: '/m/joiner.onnx'};
    var sens = +o.sens || 2.0;
    kws = K.createKws(M, {featConfig: {samplingRate: 16000, featureDim: 80},
      modelConfig: {transducer: tr, tokens: '/m/tokens.txt', numThreads: 1, provider: 'cpu', debug: 0},
      maxActivePaths: 4, numTrailingBlanks: 1, keywordsScore: sens, keywordsThreshold: +o.th || 0.2, keywords: KW.join('\n')});
    kst = kws.createStream();
    rec = A.createOnlineRecognizer(M, {featConfig: {sampleRate: 16000, featureDim: 80},
      modelConfig: {transducer: tr, tokens: '/m/tokens.txt', numThreads: 1, provider: 'cpu', debug: 0},
      decodingMethod: 'greedy_search', enableEndpoint: 1, rule1MinTrailingSilence: 1.6, rule2MinTrailingSilence: 0.8, rule3MinUtteranceLength: 12});
    rst = rec.createStream();
    ready = true;
    post({type: 'ready'});
  });
}

// новые потоки: в старом могли остаться необработанные кадры прошлой фразы
function resetAll(){
  try{ kst.free(); }catch(e){}
  try{ rst.free(); }catch(e){}
  kst = kws.createStream(); rst = rec.createStream();
  lastText = ''; sinceReset = 0;
}

function feed(pcm){
  if(!ready) return;
  sinceReset += pcm.length;
  rst.acceptWaveform(16000, pcm);
  while(rec.isReady(rst)) rec.decode(rst);
  var text = (rec.getResult(rst).text || '').trim();
  if(mode === 'wake'){
    kst.acceptWaveform(16000, pcm);
    var hit = '';
    while(kws.isReady(kst)){
      kws.decode(kst);
      var r = kws.getResult(kst);
      if(r && r.keyword){ hit = r.keyword; kws.reset(kst); break; }
    }
    if(!hit) hit = wakeIn(text);
    if(hit){
      // что сказано после слова-обращения в том же дыхании, станет началом черновика команды
      var after = text.replace(/^.*?(джарвис|жарвис|джервис|джавис|ассистент|асистент|jarvis)[а-я]*\s*/i, '');
      post({type: 'wake', word: hit, text: text});
      mode = 'cmd'; lastText = after;
      try{ rec.reset(rst); }catch(e){}
      try{ kws.reset(kst); }catch(e){}
      return;
    }
    if(rec.isEndpoint(rst) || sinceReset > 16000 * 12){ try{ rec.reset(rst); }catch(e){} sinceReset = 0; }
  } else {
    if(text) post({type: 'partial', text: (lastText + ' ' + text).trim()});
    if(rec.isEndpoint(rst)){ lastText = (lastText + ' ' + text).trim(); try{ rec.reset(rst); }catch(e){} }
  }
}

self.onmessage = function(e){
  var m = e.data || {};
  try{
    if(m.type === 'init') init(m).catch(function(err){ post({type: 'error', msg: String(err && err.message || err)}); });
    else if(m.type === 'audio') feed(m.pcm);
    else if(m.type === 'mode'){ mode = m.mode === 'cmd' ? 'cmd' : 'wake'; if(ready && mode === 'wake') resetAll(); }
    else if(m.type === 'flush'){
      var t = '';
      if(ready){ try{ while(rec.isReady(rst)) rec.decode(rst); t = (lastText + ' ' + (rec.getResult(rst).text || '')).trim(); }catch(err){} }
      post({type: 'final', text: t, id: m.id});
      mode = 'wake'; if(ready) resetAll();
    }
  }catch(err){ post({type: 'error', msg: String(err && err.message || err)}); }
};
