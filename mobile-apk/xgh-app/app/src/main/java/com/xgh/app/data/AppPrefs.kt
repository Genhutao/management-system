package com.xgh.app.data

import android.content.Context
import androidx.appcompat.app.AppCompatDelegate
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.floatPreferencesKey
import androidx.datastore.preferences.core.intPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking

private val Context.appPrefsStore by preferencesDataStore(name = "xgh_prefs")

/**
 * 应用偏好：字号缩放 + 深浅色模式。与 SessionStore 分开存，退出登录不影响。
 * 缓存值在 Application 启动时同步读一次，供 BaseActivity.attachBaseContext 与 night mode 使用。
 */
object AppPrefs {

    private val keyFontScale = floatPreferencesKey("font_scale")
    private val keyThemeMode = intPreferencesKey("theme_mode")

    /** 字号缩放：1.0 标准 / 1.15 大 / 1.3 特大 */
    @Volatile
    var fontScale: Float = 1f
        private set

    /** AppCompatDelegate.MODE_NIGHT_* ：-1 跟随系统 / 1 浅色 / 2 深色 */
    @Volatile
    var themeMode: Int = AppCompatDelegate.MODE_NIGHT_FOLLOW_SYSTEM
        private set

    fun init(context: Context) {
        runBlocking {
            val prefs = context.appPrefsStore.data.first()
            fontScale = prefs[keyFontScale] ?: 1f
            themeMode = prefs[keyThemeMode] ?: AppCompatDelegate.MODE_NIGHT_FOLLOW_SYSTEM
        }
    }

    suspend fun setFontScale(context: Context, scale: Float) {
        context.appPrefsStore.edit { it[keyFontScale] = scale }
        fontScale = scale
    }

    suspend fun setThemeMode(context: Context, mode: Int) {
        context.appPrefsStore.edit { it[keyThemeMode] = mode }
        themeMode = mode
    }
}
