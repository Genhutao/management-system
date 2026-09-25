package com.xgh.app.data

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map

private val Context.dataStore by preferencesDataStore(name = "xgh_session")

/** token / 服务器地址 / 登录用户 持久化，支持打开 App 免密直登 */
class SessionStore(private val context: Context) {

    private val keyToken = stringPreferencesKey("token")
    private val keyBaseUrl = stringPreferencesKey("base_url")
    private val keyUserName = stringPreferencesKey("real_name")
    private val keyUserBuilding = stringPreferencesKey("building")

    companion object {
        const val DEFAULT_BASE_URL = "http://103.236.77.86:19198"
    }

    val token: Flow<String?> = context.dataStore.data.map { it[keyToken] }
    val baseUrl: Flow<String> = context.dataStore.data.map { it[keyBaseUrl] ?: DEFAULT_BASE_URL }
    val userName: Flow<String?> = context.dataStore.data.map { it[keyUserName] }
    val userBuilding: Flow<String?> = context.dataStore.data.map { it[keyUserBuilding] }

    suspend fun currentBaseUrl(): String =
        normalizeBaseUrl(context.dataStore.data.first()[keyBaseUrl] ?: DEFAULT_BASE_URL)

    suspend fun currentToken(): String? = context.dataStore.data.first()[keyToken]

    suspend fun saveSession(token: String, baseUrl: String, user: User?) {
        context.dataStore.edit { prefs ->
            prefs[keyToken] = token
            prefs[keyBaseUrl] = normalizeBaseUrl(baseUrl)
            prefs[keyUserName] = user?.real_name ?: ""
            prefs[keyUserBuilding] = user?.building ?: ""
        }
    }

    suspend fun saveBaseUrl(baseUrl: String) {
        context.dataStore.edit { prefs ->
            prefs[keyBaseUrl] = normalizeBaseUrl(baseUrl)
        }
    }

    suspend fun clear() {
        context.dataStore.edit { it.clear() }
    }

    fun normalizeBaseUrl(raw: String): String {
        val trimmed = raw.trim().trimEnd('/')
        return if (trimmed.startsWith("http")) trimmed else "http://$trimmed"
    }

    /** 后端返回 /uploads/xxx 相对路径，拼成完整 URL */
    suspend fun imageUrl(raw: String?): String? {
        val path = raw?.trim() ?: return null
        if (path.isEmpty()) return null
        if (path.startsWith("http")) return path
        return currentBaseUrl() + (if (path.startsWith("/")) path else "/$path")
    }
}
