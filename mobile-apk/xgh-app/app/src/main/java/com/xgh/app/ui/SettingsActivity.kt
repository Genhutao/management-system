package com.xgh.app.ui

import android.content.Intent
import android.os.Bundle
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatDelegate
import androidx.lifecycle.lifecycleScope
import com.xgh.app.R
import com.xgh.app.XghApp
import com.xgh.app.data.ApiClient
import com.xgh.app.data.AppPrefs
import com.xgh.app.data.User
import com.xgh.app.databinding.ActivitySettingsBinding
import com.xgh.app.util.Ui
import kotlinx.coroutines.launch

class SettingsActivity : BaseActivity() {

    private lateinit var binding: ActivitySettingsBinding

    private val fontOptions = listOf(
        1f to R.string.font_standard,
        1.15f to R.string.font_large,
        1.3f to R.string.font_xlarge,
    )
    private val themeOptions = listOf(
        AppCompatDelegate.MODE_NIGHT_FOLLOW_SYSTEM to R.string.theme_follow_system,
        AppCompatDelegate.MODE_NIGHT_NO to R.string.theme_light,
        AppCompatDelegate.MODE_NIGHT_YES to R.string.theme_dark,
    )

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivitySettingsBinding.inflate(layoutInflater)
        setContentView(binding.root)

        val app = application as XghApp
        lifecycleScope.launch {
            binding.etServer.setText(app.sessionStore.currentBaseUrl())
            // profile 实时校准本地展示（读库数据）
            val profile = Ui.request(this@SettingsActivity) { ApiClient.get().service.profile() }
            if (profile != null && profile.id != 0L) renderUser(profile)
        }

        binding.toolbar.setNavigationOnClickListener { finish() }
        binding.btnFontSize.text = getString(R.string.font_size) + "：" + fontLabel()
        binding.btnThemeMode.text = getString(R.string.theme_mode) + "：" + themeLabel()
        binding.btnSaveServer.setOnClickListener {
            val url = binding.etServer.text?.toString()?.trim().orEmpty()
            if (url.isEmpty()) {
                Ui.toast(this, "请填写服务器地址")
                return@setOnClickListener
            }
            lifecycleScope.launch {
                app.sessionStore.saveBaseUrl(url)
                ApiClient.get().rebuild(app.sessionStore.currentBaseUrl())
                Ui.toast(this@SettingsActivity, "已保存")
            }
        }

        binding.btnFontSize.setOnClickListener { showFontScaleDialog() }
        binding.btnThemeMode.setOnClickListener { showThemeModeDialog() }
        binding.btnLogout.setOnClickListener {
            AlertDialog.Builder(this)
                .setMessage(R.string.logout_confirm)
                .setPositiveButton(R.string.logout) { _, _ -> doLogout() }
                .setNegativeButton(android.R.string.cancel, null)
                .show()
        }
    }

    private fun fontLabel(): String = getString(
        fontOptions.firstOrNull { it.first == AppPrefs.fontScale }?.second ?: R.string.font_standard
    )

    private fun themeLabel(): String = getString(
        themeOptions.firstOrNull { it.first == AppPrefs.themeMode }?.second
            ?: R.string.theme_follow_system
    )

    private fun showFontScaleDialog() {
        val names = fontOptions.map { getString(it.second) }.toTypedArray()
        val checked = fontOptions.indexOfFirst { it.first == AppPrefs.fontScale }
        AlertDialog.Builder(this)
            .setTitle(R.string.font_size_dialog)
            .setSingleChoiceItems(names, checked) { dialog, which ->
                dialog.dismiss()
                val scale = fontOptions[which].first
                if (scale != AppPrefs.fontScale) {
                    lifecycleScope.launch {
                        AppPrefs.setFontScale(applicationContext, scale)
                        binding.btnFontSize.text = getString(R.string.font_size) + "：" + fontLabel()
                        recreate()
                    }
                }
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun showThemeModeDialog() {
        val names = themeOptions.map { getString(it.second) }.toTypedArray()
        val checked = themeOptions.indexOfFirst { it.first == AppPrefs.themeMode }
        AlertDialog.Builder(this)
            .setTitle(R.string.theme_mode_dialog)
            .setSingleChoiceItems(names, checked) { dialog, which ->
                dialog.dismiss()
                val mode = themeOptions[which].first
                if (mode != AppPrefs.themeMode) {
                    lifecycleScope.launch {
                        AppPrefs.setThemeMode(applicationContext, mode)
                        // setDefaultNightMode 会自动重建所有 Activity
                        AppCompatDelegate.setDefaultNightMode(mode)
                        binding.btnThemeMode.text = getString(R.string.theme_mode) + "：" + themeLabel()
                    }
                }
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun renderUser(u: User) {
        binding.tvUser.text = buildString {
            append("姓名：${u.real_name ?: "-"}\n")
            append("楼栋：${u.building ?: "-"}\n")
            append("角色：${u.role ?: "-"}\n")
            u.department?.let { append("部门：$it\n") }
            u.position?.let { append("职务：$it") }
        }
    }

    private fun doLogout() {
        val app = application as XghApp
        lifecycleScope.launch {
            app.sessionStore.clear()
            ApiClient.get().currentToken = null
            Toast.makeText(this@SettingsActivity, "已退出", Toast.LENGTH_SHORT).show()
            startActivity(Intent(this@SettingsActivity, LoginActivity::class.java))
            finishAffinity()
        }
    }
}
