package com.xgh.app

import android.app.Application
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
    }
}
