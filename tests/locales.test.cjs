const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../development/simpleadmin/www/js/locales.js'),'utf8');
function locale(browser,stored,blocked=false){
 const values=new Map(stored?[['simpleadmin.language',stored]]:[]);
 const attrs={};
 const document={nodeType:9,readyState:'loading',documentElement:{setAttribute:(k,v)=>attrs[k]=v},addEventListener(){},querySelectorAll:()=>[]};
 const window={addEventListener(){},dispatchEvent(){}};
 vm.runInNewContext(source,{window,document,navigator:{language:browser},localStorage:{getItem:k=>{if(blocked)throw Error('blocked');return values.get(k)},setItem:(k,v)=>{if(blocked)throw Error('blocked');values.set(k,v)}},CustomEvent:class{}});
 return {lang:window.SimpleAdmin.Lang,attrs,values};
}
test('first visit follows supported browser language and remembers explicit preference',async()=>{
 for(const [browser,want] of [['en-GB','en'],['ru-RU','ru'],['ar-EG','ar'],['zh-TW','zh-CN'],['de-DE','zh-CN']]){
  const {lang,attrs}=locale(browser);assert.equal(lang.getCurrentLanguage(),want);await lang.load();assert.equal(attrs.dir,want==='ar'?'rtl':'ltr');
 }
 const {lang,values,attrs}=locale('en-US','ru');await lang.load();assert.equal(lang.getCurrentLanguage(),'ru');
 await lang.setLanguage('ar');assert.equal(values.get('simpleadmin.language'),'ar');assert.equal(attrs.dir,'rtl');
 await lang.setLanguage('zh-CN');assert.equal(attrs.dir,'ltr');
});
test('storage failure does not block login language selection',async()=>{const {lang}=locale('ar-EG',null,true);await lang.load();await lang.setLanguage('ru');assert.equal(lang.getCurrentLanguage(),'ru');});
test('dynamic statuses and chart labels translate for every non-Chinese language',async()=>{
 for(const code of ['en','ru','ar']){const {lang}=locale(code);for(const key of ['登录','当前延迟','2 / 3 应答','失败 1 次','已激活卡1','等待 NR 信号数据','活动频段: NR5G BAND 77','温度','抖动'])assert(!/[\u4e00-\u9fff]/.test(lang.t(key)),code+': '+key);}
});
