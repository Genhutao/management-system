package com.xgh.app

import android.app.Application
import android.content.Intent
import com.xgh.app.data.ApiClient
import com.xgh.app.data.SessionStore
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch

class XghApp : Application() {

    val sessionStore: SessionStore by lazy { SessionStore(this) }
    val appScope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    override fun onCreate() {
        super.onCreate()
        val api = ApiClient.get()
        appScope.launch {
            // 启动时把存储的服务器地址与 token 同步进网络层
            val baseUrl = sessionStore.currentBaseUrl()
            api.rebuild(baseUrl)
            api.currentToken = sessionStore.currentToken()
        }
        // token 失效/被吊销：清会话并整体回到登录页，避免停在原页面反复报错
        api.onUnauthorized = {
            appScope.launch { sessionStore.clear() }
            api.currentToken = null
            val intent = Intent(this, LoginActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
            startActivity(intent)
        }
    }
}
