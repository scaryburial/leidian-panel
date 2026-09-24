import { chromium } from 'playwright';
const base='http://127.0.0.1:33441/ui3344';
const b=await chromium.launch({args:['--no-sandbox']});
const p=await b.newPage({viewport:{width:1200,height:900}});
try{
 await p.goto(base+'/',{waitUntil:'networkidle',timeout:30000});
 await p.waitForTimeout(1200);
 const i=p.locator('input'); await i.nth(0).fill('3344'); await i.nth(1).fill('3344');
 await p.locator('button[type=submit]').first().click(); await p.waitForTimeout(4500);
 await p.goto(base+'/panel/relay',{waitUntil:'domcontentloaded',timeout:30000});
 await p.waitForTimeout(3000);
 await p.screenshot({path:'/root/shot-relay.png',fullPage:true});
 console.log('ok',p.url());
}catch(e){console.log('ERR',e.message);}finally{await b.close();}
